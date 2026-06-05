# Tech Spec: user-traffic-simulator

> Source: brief-file (01-brief.md) + requirements (00-requirements.md) + 사용자 결정 배치 1~3 (2026-06-05)

가상 유저 상태 그래프를 로컬에서 실행해 SUT를 시나리오/이탈/부하 3모드로 두드리고,
클라이언트(+opt-in 서버) 신호로 이슈를 표면화하는 **로컬 우선 + 분산 확장형** 트래픽 실험 도구.
백엔드 Go 단일 바이너리(React UI 임베드), 대용량 시 분산 워커 배포.

---

## 아키텍처

### WHAT — 외부에서 보이는 행동

- **로컬 실행**: 단일 Go 바이너리 실행 → `localhost` 웹 UI 오픈. JVM/런타임 설치 불요 (k6/Locust 스타일).
- **3 사용자군 동선**: 운영자(개발자/기획자) = 실험 설정·실행 / 뷰어(디자이너/기획자) = 공유 리포트 URL 읽기.
- **실험 정의 외부 계약**:
  - SUT API + payload 템플릿 등록 (REST 기본; 프로토콜 어댑터로 gRPC/WS 확장 → AD-004)
  - 행동 상태 그래프 작성: 노드(상태)+전이확률+**의존 엣지**(필수 선행) — 파일(YAML/JSON) 또는 UI 에디터. 경량 모드=가중 랜덤워크 (AD-003)
  - 파라미터: 가상유저 수 / 이탈 확률(%) / 부하 집중 대상+프로파일(가중·램프·스파이크) / 인증 전략 / 대상 환경
- **실행 결과**: 실시간 진행 지표 + 종료 후 리포트(시나리오 준수 / 이탈 / 부하 집중 3모드 동일 축 병치 + 분류된 이슈 목록).
- **대용량 모드**: master + N workers 분산 배포(AD-007). 동일 실험 정의를 분산 실행.
- **안전 계약**: dev/staging 화이트리스트 밖 대상 거부, 하드 rate cap, kill switch(수동 상시 + 자동 opt-in) (AD-008).

### HOW — 내부 구현 결정

- 채택 AD: AD-001(폼팩터) · AD-002(스택 Go+TS) · AD-003(행동모델 상태그래프) · AD-004(프로토콜 어댑터) · AD-005(이탈+변형) · AD-006(부하전략 플러그인) · AD-007(분산 스케일) · AD-008(안전장치) · AD-009(인증 플러그형) · AD-010(저장소 모드별) · AD-011(PII 마스킹) · AD-012(관측 계층) · AD-013(접근 모델)
- **컴포넌트 (모두 Go, UI만 React)**:
  - `Control Plane` — REST API + React UI(embed.FS) 서빙. 실험 CRUD, 실행 오케스트레이션.
  - `Scenario Graph Engine` — 상태 그래프 해석. 전이 시 의존 엣지 검증(불가침) → 확률적 이탈/재정렬/변형 적용(AD-005).
  - `Virtual User Runtime` — 가상유저 1명 = goroutine. 인증 컨텍스트(AD-009) 보유, 그래프를 순회하며 SUT 호출.
  - `Load Coordinator` — 부하 프로파일(AD-006)을 워커에 배분. 로컬=in-process, 분산=master→workers(AD-007).
  - `Observation Collector` — 클라이언트 지표 필수 + 서버측 메트릭 opt-in 병합(AD-012).
  - `Safety Guard` — 화이트리스트·rate cap·kill switch(AD-008). 모든 송신 경로 앞단.
  - `Report/Issue Aggregator` — 메트릭 집계 + 이슈 분류(임계 위반/계약 위반/변형 적발).
- **데이터 흐름**: 사용자 → Control Plane → (그래프+파라미터 검증) → Load Coordinator → Virtual User Runtime(워커) → [Safety Guard] → SUT → 응답/지표 → Collector → Store(AD-010) → Aggregator → 리포트.
- 미해결: 없음 (배치 1~3에서 9건 전부 확정). 잔여 운영 TODO는 AD-011(마스킹 도구) 참조.

## 데이터 모델

- `Experiment`: id, name, target_env_ref, scenario_graph_ref, params(virtual_user_count, deviation_rate, auth_strategy), created_at.
- `TargetEnv`: id, base_url, **allowlist**(host 패턴), rate_cap(max RPS/conn), env_class(dev|staging|*prod-locked*).
- `ScenarioGraph`: id, nodes[], edges[]. `Node`: id, api_template_ref, transition_weights. `Edge`: from, to, **dependency**(bool — true면 선행 필수, 이탈로 건너뛰기 불가).
- `ApiTemplate`: id, method, path, headers, payload_template(필드 + 변형 규칙 ref), protocol(rest|grpc|ws — AD-004).
- `CredentialPool`: id, strategy(pool|bootstrap-signup), entries[](JWT|session|member-info, **masked at rest** — AD-011), bootstrap_flow_ref(가입 선행 그래프, nullable).
- `LoadProfile`: id, target_api_ref, strategy(weight|ramp|spike|soak — AD-006), shape(시계열 파라미터).
- `RunExecution`: id, experiment_ref, mode(local|distributed), started_at, ended_at, status, kill_reason(nullable).
- `MetricSample`: run_ref, ts, api_ref, status_code, latency_ms, error_class, worker_id (고빈도 → TSDB, AD-010).
- `Finding(Issue)`: run_ref, category(threshold|contract|mutation|availability), severity, evidence_ref, first_seen_ts.
- `ReportShare`: id, run_ref, token(opaque — 보유=열람), scope(read-only), created_at, expires_at(nullable, 만료) — 뷰어 공유용 (AD-013).
- `AccessRole`: operator(로컬 컨트롤플레인 전체) | viewer(share-token 보유 시 리포트 read-only) (AD-013).

## 인터페이스

- **Control Plane REST API** (내부 UI + 자동화용):
  - `POST /experiments`, `GET /experiments/{id}`, `POST /experiments/{id}/run`, `POST /runs/{id}/kill`, `GET /runs/{id}/report`(operator), `GET /runs/{id}/stream`(SSE 실시간).
  - `POST /runs/{id}/share` → share-token/URL 발급 / `GET /reports/shared/{token}` → 뷰어 read-only 리포트(토큰 보유로만, PII는 AD-011 마스킹 적용) (AD-013).
- **Scenario Graph 포맷** (YAML/JSON): nodes/edges/transition_weights/dependency. UI 에디터와 파일 1:1 호환.
- **Protocol Adapter interface** (AD-004): `Send(ctx, ApiTemplate, creds) → Response{status, latency, body}`. REST 구현 v1, gRPC/WS는 동일 인터페이스 후속.
- **Auth Provider interface** (AD-009): `Acquire(virtualUserId) → Credential`. 구현: `PoolProvider`(사전 주입), `BootstrapSignupProvider`(가입 선행).
- **Load Strategy plugin** (AD-006): `Schedule(profile, t) → concurrency/rate`. 구현: weight, ramp, spike, soak. (RPS 절대값=후속).
- **Worker coordination** (분산, AD-007): master ↔ worker gRPC. 실험 정의 push, 지표 pull/stream.
- **Report schema** (JSON): per-mode 지표(요청수, 성공/실패율, status 분포, p50/p95/p99, timeout, error_rate) + findings[].

## 마이그레이션

- **그린필드** — 기존 스키마/시스템 마이그레이션 해당 없음.
- **배포 형태 전환** (로컬 ↔ 분산, AD-007): 로컬=단일 바이너리 in-process 워커 + 임베디드 스토어. 분산=동일 바이너리를 `--role=master|worker`로 기동 + 외부 스토어(Postgres/TSDB/큐, AD-010).
- **저장소 모드 전환** (AD-010): 단일노드 임베디드(SQLite/Parquet) → 분산 외부 스토어. 스키마 동일, 드라이버 교체.
- **롤백**: 실험 실행은 무상태에 가까움 — kill switch로 즉시 중단, 설정/부분 결과 보존. 도구 자체 배포 롤백 = 이전 바이너리로 교체. **Zero-downtime: 해당 약함**(실험 런은 1회성, 상시 서비스 아님).

## 관측성

> 주의: 본 도구의 관측성은 *두 층*. (1) **SUT 관측**=제품 가치(AD-012) (2) **도구 자체** 관측=운영.

- **SUT 관측(제품)**: 클라이언트측 필수 — status 분포, p50/p95/p99 지연, timeout 수, error_rate, 응답 assertion 결과. 서버측 opt-in — Prometheus/OTel/APM/로그 pull로 근본원인 보강.
- **이슈 판정**: threshold(에러율/지연 임계 초과), contract(응답 스키마/상태 불일치), mutation(변형 입력이 유발한 4xx/5xx — goal #4), availability(연속 timeout/5xx = 포화·다운 신호 → goal #3).
- **Kill switch 트리거(AD-008)**: 수동(상시) + 자동(opt-in, 기본 보수적/비활성 — 포화 관측을 끊지 않도록): error_rate>임계 / 5xx 급증 / timeout 폭증.
- **도구 자체 메트릭**: 워커별 실제 송신 RPS·동시성(설정값 대비 추종 오차), goroutine 수, 메모리, 큐 lag(분산). 알람: 워커 추종 오차>10% → 리포트 경고.
- **로그**: 실험 start/kill, 화이트리스트 거부, rate cap 발동, 자동 kill 발화 — 모두 구조화 로그(민감 필드 마스킹, AD-011).

## 결정 카드

> 배치 1~3에서 사용자가 직접 선택. 옵션 풀 전개는 `00-requirements.md`(Q-004~Q-010) 참조, 본 카드는 *확정안 + 근거 + Steelman + status*.

### AD-001 — 폼팩터: 로컬 우선 + 분산 확장
- blocks: `implement`
- priority: **P0** · depends-on: —
- 근거: 01-brief.md 목표 2(비전문가 셀프서비스)=로컬 간편 실행, 목표 1·goal #3(대용량)=분산 필요. Locust 선례가 둘을 한 도구로.
- 확정: **로컬 단일 바이너리 + localhost 웹 UI(React 임베드) + 옵션 분산 워커 배포**. 호스팅 SaaS 아님.
- 권장 근거: k6/Locust가 정확히 이 모델. 순수 SaaS는 "받아서 로컬 실행"(사용자 명시)과 충돌, 순수 로컬-only는 goal #3 대용량을 단일 머신으로 못 냄 → 비대칭적으로 hybrid.
- Steelman: 로컬+분산 둘 다 지원은 코드 경로 2개 → 유지비↑. 완화: 동일 바이너리 `--role` 분기로 분기 최소화.
- status: **user-overridden** (Q-001=B를 "로컬 실행 도구"로 정밀화)

### AD-002 — 스택: Go(백엔드) + TypeScript/React(UI)
- blocks: `implement`
- priority: **P0** · depends-on: AD-001, AD-007
- 근거: 로컬 단일 바이너리(AD-001) + 분산 대용량 워커(AD-007) 양쪽이 경량 런타임 선호.
- 옵션: A=Kotlin-only / B=Kotlin엔진+Go워커(3언어) / **C=Go-only + TS UI** ⭐
- 확정: **C (Go 엔진+워커 단일 바이너리, React UI를 embed.FS로 임베드)**.
- 권장 근거: 사용자 핵심 2요구(로컬 k6식 단일바이너리 + 분산 경량 워커)가 둘 다 Go를 가리킴. 사용자 레퍼런스 k6가 Go. KotlinGo(3언어)는 자충수. Kotlin-only는 JVM이라 단일바이너리 배포·워커 밀도에서 불리. 유일 비용=팀 Go 램프업이나 Go는 학습이 빠르고 부하도메인에 idiomatic.
- Steelman: 팀이 Kotlin/TS 전문 → 초기 속도는 Kotlin이 빠름. 단 부하도구를 JVM으로 끌고 가는 장기 비용이 더 큼 — 수용.
- status: **user-overridden** (D8 = Go-only)

### AD-003 — 행동 모델: 명시적 상태 그래프(+경량 랜덤워크)
- blocks: `implement`
- priority: **P0** · depends-on: —
- 근거: 요구사항 "정해진 틀"=상태/전이, "프로세스 건너뛰기 금지"=의존 엣지로 매핑(00-requirements.md Q-002).
- 확정: 상태 그래프(노드+전이확률+**의존 엣지**), 경량 진입점=가중 랜덤워크.
- 권장 근거: 녹화(B)는 "실유저 없음" 전제와 모순, LLM유저(C)는 대용량서 비용 폭발. 그래프가 제약을 1급으로 표현 + 결정적·확장.
- Steelman: 그래프 작성이 비개발자에 부담 → 템플릿 갤러리 + 비주얼 에디터 + 경량 모드로 완화.
- status: accepted-default (Q-002=A)

### AD-004 — 프로토콜: REST 기본 + 어댑터 확장
- blocks: `implement` · priority: P1 · depends-on: —
- 확정: **HTTP/REST v1 + `ProtocolAdapter` 인터페이스**로 gRPC/WS/SSE 확장 가능(사용자 API 양식에 맞춰).
- 권장 근거: 원문 "API+payload"=REST. 사용자 명시("C가 결국 가능해야") → 어댑터 추상화로 재작성 없이 확장. 지금 모든 예시는 REST가 커버.
- Steelman: 어댑터 추상화를 너무 일찍 빼면 REST 전용 최적화를 놓침 → 인터페이스는 얇게, REST 구현은 직접 최적화.
- status: **user-overridden** (D1 = A + 확장성)

### AD-005 — 이탈 + 입력 변형 주입
- blocks: `implement` · priority: P1 · depends-on: AD-003
- 확정: 선택단계 생략/독립단계 재정렬/이탈 (의존엣지 불가침) **+ payload 변형/경계값/악의 입력 주입**(퍼징-라이트).
- 권장 근거: goal #4(놓친 에러)는 정상 순서 아닌 예상외 입력에서 발생 → 변형 주입이 직격. 완전 카오스(C)는 원문 제약 위반이라 제외.
- Steelman: 변형이 오탐 양산 → "진짜 이슈"가 묻힘. 완화: 변형 강도 단계 + 판정 규칙(계약 위반만 이슈화).
- status: accepted-default (Q-005=B)

### AD-006 — 부하 집중: 가중+램프 프로파일(전략 플러그인)
- blocks: `implement` · priority: P1 · depends-on: AD-003
- 확정: 가중 배수 + 램프/스파이크/soak 프로파일. `LoadStrategy` 플러그인으로 절대 RPS(B) 후속 추가 가능.
- 권장 근거: "특정 api 막 몰리게"=상대 강조, goal #3 스케일테스트=시간축 형태 필수. 절대 RPS(B)는 찾으려는 서버 한계를 미리 알아야 하는 모순.
- Steelman: 가중은 정확한 SLA RPS 검증 부적합 → 전략 플러그인으로 B 후속.
- status: **user-overridden** (D3 = A+D + 플러그인)

### AD-007 — 스케일: 분산 스케일아웃 + 로컬 단일노드 모드
- blocks: `implement` · priority: P1 · depends-on: —
- 확정: 코디네이터 + **무상태 워커**. 로컬=in-process 단일노드(**목표 ~2,000 동시**), 분산=master+workers(**목표 10,000+ 동시 / 50,000+ RPS**). 동일 바이너리 `--role`. (capacity target — spec-analyze G2 해소, qa-plan 부하검증 기준)
- 권장 근거: 사용자 "대용량 트래픽 경험"(goal #3)이 핵심 → 분산 필수. 단 단일노드 모드로 "단순 시작" 유지. 무상태 워커라 확장이 설정 변경.
- Steelman: 처음부터 분산은 과설계 위험 → 로컬 모드를 기본·기본 검증 경로로 두고 분산은 opt-in.
- status: **user-overridden** (D4 = C 방향, 단 로컬 모드 병행)

### AD-008 — 안전장치: dev/staging 기본 + 가드 + kill switch
- blocks: `implement` · priority: P1 · depends-on: —
- 확정: 호스트 화이트리스트 + 하드 rate cap + kill switch(수동 상시 + 자동 opt-in/보수적). prod는 정책 §1 트라이어드 갖춘 명시 잠금해제로만.
- 권장 근거: 트래픽을 일부러 몰리게 하는 도구라 오발사=self-DoS. 정책 §1·§5.2 직접 적용. 가드 비용 작고 파국 방지.
- Steelman: 자동 kill이 민감하면 "서버 죽는 순간"(goal #3)을 관측 전에 끊음 → 자동은 기본 비활성/높은 임계 + 사용자 조정.
- status: **user-overridden** (D5=A, kill=수동+자동opt-in 조정가능)

### AD-009 — 인증: 플러그형(사전 풀 + 부트스트랩 가입)
- blocks: `implement` · priority: P1 · depends-on: AD-003
- 확정: `AuthProvider` 인터페이스. (1) **사전 자격증명 풀**(기존 회원/JWT/세션 주입) (2) **부트스트랩 회원가입 단계**(트래픽 인원수만큼 가입을 선행 노드로). 둘 다 설정.
- 권장 근거: 사용자 명시 — "회원가입부터 시키거나, 기존 회원 JWT/세션 주입". 순수 풀은 가입경로 테스트 불가, 순수 가입은 매 런 비용·rate limit. 플러그형이 양쪽 수용.
- Steelman: 플러그형은 설정 표면↑ → 토큰풀을 기본값, 가입은 opt-in 선행 그래프로.
- status: **user-overridden** (D6=C + 2전략)

### AD-010 — 저장소: 모드별(로컬 임베디드 / 분산 Postgres+TSDB+큐)
- blocks: `implement` · priority: P1 · depends-on: AD-007
- 확정: 로컬=임베디드(SQLite 설정/결과 + Parquet/임베디드 시계열). 분산=Postgres(설정/결과) + TSDB(TimescaleDB/ClickHouse, 고빈도 메트릭) + 큐(NATS/Redis, 워커 fan-out).
- 권장 근거: D4 분산 대용량 → 메트릭 write 초당 수만 → 올인원 Postgres 병목. 단일노드는 임베디드로 축소해 "단순 시작".
- Steelman: TSDB+큐는 운영 컴포넌트↑ → 모드별 분리로 작은 실험은 경량 유지.
- status: accepted-default (D9=C)

### AD-011 — PII: 실데이터 허용 + 필드 마스킹(deny-by-default)
- blocks: `implement` · priority: P1 · depends-on: AD-009
- 근거: AD-009가 실회원 JWT/세션(민감정보) 주입을 허용 → 실데이터 불가피. 정책 §5.3 PII 노출 금지.
- 확정: 실데이터 허용 + 저장/로그/리포트에서 **필드 지정 자동 마스킹**, 미지정 의심 필드도 가림(deny-by-default).
- 권장 근거: A(실데이터 금지)는 AD-009와 모순. §5.3은 마스킹으로 충족.
- 잠금 해제(잔여): 정책 §5.3 마스킹 *도구*가 TODO(미정) → v1은 자체 필드 마스킹, 도구 확정 시 교체.
- Steelman: 자체 마스킹은 누락 위험 → deny-by-default로 보수적.
- status: **user-overridden** (D7=B, 자체 마스킹 deny-by-default)

### AD-012 — 관측 계층: 클라이언트 필수 + 서버 opt-in
- blocks: `implement` · priority: P1 · depends-on: —
- 확정: 클라이언트측 지표 코어 + 서버측 메트릭(Prometheus/OTel/APM/로그) opt-in.
- 권장 근거: goal #3·#4 *증상*은 클라이언트로 포착 → 전원 즉시 사용. 서버측 선배선 필수(순수 B)는 비코더·외부 SUT 배제.
- Steelman: 클라만으론 근본원인 추론에 그침 → 서버 opt-in 레이어로 보강.
- status: accepted-default (Q-003=A+B)

### AD-013 — 접근 모델: operator 전체 / viewer share-token read-only
- blocks: `implement` · priority: P1 · depends-on: AD-001
- 근거: brief 유저플로우(뷰어=공유 리포트 읽기, `01-brief.md` `## 유저 플로우`)가 인터페이스에 미반영 (spec-analyze G1).
- 확정: operator=로컬 컨트롤플레인 전체 권한 / viewer=share-token 보유 시 리포트 read-only. 토큰 opaque + 선택적 만료.
- 권장 근거: brief 목표 2(기획자·디자이너 셀프서비스)는 리포트 *소비* 동선이 필요. 로컬 설치형 도구라 무거운 IAM/인증서버 대신 share-token이 비대칭적으로 적합(인증 인프라 불요).
- Steelman: share-token URL 유출 시 리포트 노출 → 만료 + (옵션) 1회용 토큰 + 리포트 내 PII는 AD-011 마스킹으로 이미 가려짐.
- status: user-overridden (G1 해소, 표준 share-token 모델 채택)
