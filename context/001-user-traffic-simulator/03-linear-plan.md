# Linear Plan: user-traffic-simulator  (GitHub Issues 타깃)

> mode: **created** (GitHub 이슈 28개 생성 완료 — 2026-06-05)
> source: tech-spec (02-tech-spec.md AD 1~13) + brief + requirements
> target: GitHub repo `chordpli/tmula` (private, 이슈 활성)
> tracker: GitHub Issues (Linear 미사용 — Epic=트래킹 이슈+체크리스트, 계층=라벨)
> generated_at: 2026-06-05
>
> **이슈 번호 매핑**: Issue T1~T21 = GitHub **#1~#21** (T번호=이슈번호) · Epic E1~E7 = **#22~#28**
> - E1 #22(→#1·#2·#3) · E2 #23(→#4·#5) · E3 #24(→#6·#7·#8) · E4 #25(→#9·#10·#13) · E5 #26(→#11·#12·#14) · E6 #27(→#15·#16·#17·#18) · E7 #28(→#19·#20·#21)
> - 라벨: epic / wave-0~4 / area-* / P0·P1·P2 (총 18개 생성)
> - URL: https://github.com/chordpli/tmula/issues

## 라벨 모델 (생성 시 함께 생성)

- 계층: `epic`
- Wave(의존 순서): `wave-0` `wave-1` `wave-2` `wave-3` `wave-4`
- 영역: `area-foundation` `area-engine` `area-load` `area-safety` `area-auth` `area-obs` `area-storage` `area-ui` `area-infra`
- 우선순위: `P0` `P1` `P2`

## Epic (트래킹 이슈 7개)

| Epic | 제목(명사구) | 묶는 Issue | appetite |
|---|---|---|---|
| E1 | 프로젝트 토대 & 코어 도메인 | T1, T2, T3 | 1 cycle |
| E2 | 시나리오 그래프 엔진 | T4, T5 | 1 cycle |
| E3 | 부하 생성 & 가상 유저 런타임 | T6, T7, T8 | 1 cycle |
| E4 | 안전장치 & 인증 | T9, T10, T13 | 1 cycle |
| E5 | 관측·이슈 판정 & 저장 | T11, T12, T14 | 1 cycle |
| E6 | 컨트롤 플레인 & 웹 UI | T15, T16, T17, T18 | 1.5 cycle |
| E7 | 분산 확장 & 부하 검증 | T19, T20, T21 | 1.5 cycle |

각 Epic 본문: `## 목표` / `## 범위`(자식 체크리스트) / `## 비범위` / `## 성공 기준`.

## Issues (21개)

### T1 — [infra] 프로젝트 스캐폴딩 & 빌드/테스트 파이프라인
- 배경/목적: 그린필드 시작. Go 모듈 + React 워크스페이스 + CI. policy §7 빌드/테스트 명령 정의.
- 변경 사항: Go module, 디렉토리(cmd/engine, internal/*, web/), Makefile(build/test/lint), GitHub Actions CI, embed.FS 골격.
- AC:
  - [ ] `make build` 단일 바이너리 산출 (React 정적 자산 임베드 골격 포함)
  - [ ] `make test` / `make lint` 통과 (빈 테스트라도 그린)
  - [ ] CI가 PR에서 build+test 실행
- 의존성: 없음 (root)
- 메타: 추정 1d · P0 · labels: `wave-0` `area-foundation` `P0` · AD-002

### T2 — [feat] 코어 도메인 모델 정의
- 배경/목적: tech-spec `## 데이터 모델` 엔티티를 Go 타입으로. 전 컴포넌트의 토대.
- 변경 사항: Experiment, TargetEnv(allowlist/rate_cap/env_class), ScenarioGraph(Node/Edge+dependency), ApiTemplate(protocol enum), CredentialPool, LoadProfile, RunExecution, MetricSample, Finding, ReportShare, AccessRole.
- AC:
  - [ ] 11개 엔티티 구조체 + 검증(필수필드/enum) 단위 테스트
  - [ ] Edge.dependency=true 직렬화/역직렬화 라운드트립 테스트
  - [ ] env_class에 prod-locked 플래그 존재
- 의존성: blocked-by T1
- 메타: 추정 2d · P0 · labels: `wave-0` `area-foundation` `P0` · AD 데이터모델

### T3 — [feat] 그래프 정의 포맷 (YAML/JSON) 파서·검증
- 배경/목적: 사용자가 행동 그래프를 파일로 작성(UI와 1:1). 의존엣지+전이확률 표현.
- 변경 사항: 스키마, 파서, 검증(사이클·고아노드·전이확률 합·의존엣지 정합).
- AC:
  - [ ] 유효 그래프 파싱 성공 + 라운드트립
  - [ ] 잘못된 그래프(전이확률>1, 의존 사이클) 거부 + 명확한 에러
  - [ ] 의존엣지가 위상정렬 가능함을 검증
- 의존성: blocked-by T2
- 메타: 추정 2d · P0 · labels: `wave-0` `area-engine` `P0` · AD-003

### T4 — [feat] 시나리오 그래프 실행 엔진
- 배경/목적: 가상 유저가 그래프를 순회. 의존엣지 불가침 + 전이확률 기반 이동.
- 변경 사항: 상태 순회기, 전이 선택, 의존 선행 검증(미충족 시 해당 전이 차단).
- AC:
  - [ ] 전이확률 분포가 다회 실행에서 통계적으로 일치
  - [ ] 의존 선행 미충족 노드로의 전이 0건(불가침 검증 테스트)
  - [ ] 그래프 종료 조건 도달 시 정상 종료
- 의존성: blocked-by T3
- 메타: 추정 3d · P0 · labels: `wave-1` `area-engine` `P0` · AD-003

### T5 — [feat] 랜덤 이탈 + 입력 변형 주입
- 배경/목적: goal #2·#4. 확률적 생략/재정렬/이탈 + payload 변형(경계·타입·누락) — 의존엣지는 준수.
- 변경 사항: 이탈 확률 엔진, payload mutator(필드 변형/경계값), 변형 강도 단계.
- AC:
  - [ ] 설정 이탈% ≈ 실제 분포(±오차) + 의존엣지 위반 0
  - [ ] 변형이 정상순서로 안 나오던 4xx/5xx를 유발(샘플 SUT로 검증)
  - [ ] 변형 on/off 토글 + 강도 설정 동작
- 의존성: blocked-by T4
- 메타: 추정 3d · P1 · labels: `wave-1` `area-engine` `P1` · AD-005

### T6 — [feat] 프로토콜 어댑터 인터페이스 + REST 구현
- 배경/목적: SUT 호출 추상화. v1 REST, gRPC/WS 확장 여지.
- 변경 사항: `ProtocolAdapter.Send(ctx, ApiTemplate, creds)→Response`, REST(net/http) 구현, 헤더/payload 템플릿 치환.
- AC:
  - [ ] REST GET/POST/PUT/DELETE 호출 + status/latency/body 수집
  - [ ] payload 템플릿 변수 치환 동작
  - [ ] 인터페이스가 gRPC/WS 추가에 열려있음(미구현 stub 컴파일)
- 의존성: blocked-by T2
- 메타: 추정 2d · P0 · labels: `wave-1` `area-load` `P0` · AD-004

### T7 — [feat] 가상 유저 런타임 (goroutine per user)
- 배경/목적: 가상 유저 1명=goroutine. 인증 컨텍스트 보유, 그래프 순회하며 어댑터로 SUT 호출.
- 변경 사항: VirtualUser 실행 루프, 동시성 풀, 컨텍스트 취소(kill 연동), 가상유저 수 파라미터.
- AC:
  - [ ] N명 동시 실행 + graceful 취소
  - [ ] 각 유저가 독립 인증/세션 컨텍스트 유지
  - [ ] 로컬 ~2,000 동시 유저 무리 없이 기동(스모크)
- 의존성: blocked-by T4, T6
- 메타: 추정 3d · P0 · labels: `wave-1` `area-load` `P0` · 아키텍처 HOW

### T8 — [feat] 부하 전략 플러그인 + 가중/램프/스파이크/soak
- 배경/목적: 특정 API에 트래픽 집중(가중) + 시간축 프로파일(goal #3 스케일).
- 변경 사항: `LoadStrategy.Schedule(profile,t)→concurrency/rate`, weight/ramp/spike/soak 구현.
- AC:
  - [ ] 가중 배수대로 대상 API 호출 비율 집중
  - [ ] ramp/spike 시계열이 의도 형태 추종(±10%)
  - [ ] 전략 플러그인으로 절대 RPS(후속) 추가 가능 구조
- 의존성: blocked-by T7
- 메타: 추정 2d · P1 · labels: `wave-1` `area-load` `P1` · AD-006

### T9 — [feat] Safety Guard (화이트리스트 + rate cap + kill switch)
- 배경/목적: 트래픽 플러더라 오발사=self-DoS. 정책 §1·§5.2. 모든 송신 앞단 가드.
- 변경 사항: 호스트 화이트리스트(dev/staging), 하드 rate cap, kill switch(수동 상시 + 자동 임계 opt-in/보수적), prod-locked 거부.
- AC:
  - [ ] 화이트리스트 밖 대상 송신 0(거부+로그)
  - [ ] rate cap 초과 시 송신 차단
  - [ ] 수동 kill 즉시 전 워커 중단 / 자동 임계(opt-in) 발화 동작
- 의존성: blocked-by T7
- 메타: 추정 2d · P0 · labels: `wave-2` `area-safety` `P0` · AD-008

### T10 — [feat] Auth Provider (토큰풀 + 부트스트랩 가입)
- 배경/목적: 인증 서비스 대상. 기존 회원 JWT/세션 주입 또는 트래픽 인원만큼 사전 가입.
- 변경 사항: `AuthProvider.Acquire(userId)→Credential`, PoolProvider(주입), BootstrapSignupProvider(가입 선행 그래프).
- AC:
  - [ ] 토큰풀에서 유저별 자격증명 배분
  - [ ] 부트스트랩 가입이 트래픽 인원수만큼 선행 실행
  - [ ] 자격증명은 마스킹되어 저장(T13 연동)
- 의존성: blocked-by T7
- 메타: 추정 3d · P1 · labels: `wave-2` `area-auth` `P1` · AD-009

### T11 — [feat] Observation Collector (클라이언트 지표)
- 배경/목적: goal #1·#3 증상 포착. status/지연/timeout/error_rate 수집.
- 변경 사항: 지표 수집 파이프라인, p50/p95/p99 집계, MetricSample 적재.
- AC:
  - [ ] 요청별 status/latency/error_class 기록
  - [ ] 백분위(p50/95/99)·error_rate·timeout 집계 정확
  - [ ] 응답 assertion 결과 수집
- 의존성: blocked-by T7
- 메타: 추정 2d · P0 · labels: `wave-2` `area-obs` `P0` · AD-012

### T12 — [feat] 이슈 판정 & Aggregator
- 배경/목적: "이슈를 찾아낸다" 핵심. 4분류 판정.
- 변경 사항: Finding 분류기 — threshold(임계초과)/contract(스키마·상태불일치)/mutation(변형 적발)/availability(연속 timeout·5xx).
- AC:
  - [ ] 4 카테고리 각 1건 이상 합성 케이스로 탐지 검증
  - [ ] severity 부여 + evidence_ref 링크
  - [ ] 3모드(준수/이탈/부하) 지표 동일 축 집계
- 의존성: blocked-by T11
- 메타: 추정 3d · P1 · labels: `wave-2` `area-obs` `P1` · AD-012

### T13 — [feat] PII 필드 마스킹 (deny-by-default)
- 배경/목적: 정책 §5.3. 실회원 토큰/PII가 로그·리포트·저장에 노출 방지.
- 변경 사항: 필드 지정 마스킹 + 미지정 의심필드 가림(deny-by-default), 로그/리포트/저장 경로 적용.
- AC:
  - [ ] 지정 필드(토큰/이메일/전화) 마스킹
  - [ ] 미지정 의심 필드도 기본 가림
  - [ ] 로그·리포트·저장 3경로 모두 적용 검증
- 의존성: blocked-by T2
- 메타: 추정 2d · P1 · labels: `wave-2` `area-safety` `P1` · AD-011

### T14 — [feat] 저장소 추상화 + 로컬 임베디드(SQLite)
- 배경/목적: 모드별 저장. 로컬=임베디드 경량.
- 변경 사항: Store 인터페이스(설정/결과/메트릭), SQLite 구현, 메트릭 임베디드 시계열.
- AC:
  - [ ] 실험/런/결과 CRUD + 조회
  - [ ] 메트릭 고빈도 쓰기 로컬 수용
  - [ ] Store 인터페이스가 분산(Postgres+TSDB) 교체 가능
- 의존성: blocked-by T2
- 메타: 추정 2d · P1 · labels: `wave-2` `area-storage` `P1` · AD-010

### T15 — [feat] Control Plane REST API
- 배경/목적: UI·자동화 진입점. 실험 CRUD·실행·중단·리포트·스트림.
- 변경 사항: `POST /experiments`,`GET /experiments/{id}`,`POST /experiments/{id}/run`,`POST /runs/{id}/kill`,`GET /runs/{id}/report`(operator),`GET /runs/{id}/stream`(SSE).
- AC:
  - [ ] 전 엔드포인트 동작 + 입력 검증
  - [ ] SSE로 실시간 진행 지표 스트림
  - [ ] kill 엔드포인트가 Safety Guard 연동
- 의존성: blocked-by T7, T11
- 메타: 추정 3d · P0 · labels: `wave-3` `area-obs` `P0` · 인터페이스

### T16 — [feat] 리포트 스키마 + share-token 접근 모델
- 배경/목적: 결과 전달 + 뷰어 read-only 공유(AD-013, spec-analyze G1).
- 변경 사항: 리포트 JSON 스키마, `POST /runs/{id}/share`, `GET /reports/shared/{token}`, ReportShare(만료), AccessRole.
- AC:
  - [ ] 3모드 지표 + findings 포함 리포트 생성
  - [ ] share-token 발급 + 토큰 보유로만 read-only 접근
  - [ ] 공유 리포트에 PII 마스킹 적용(T13)
- 의존성: blocked-by T12, T15
- 메타: 추정 2d · P1 · labels: `wave-3` `area-obs` `P1` · AD-013

### T17 — [ui] React 웹 UI (설정·그래프 에디터·실시간·리포트)
- 배경/목적: 목표 2 비개발자 셀프서비스. Go 바이너리에 임베드.
- 변경 사항: 실험 설정 폼, 그래프 비주얼 에디터(또는 파일 업로드), 실시간 진행, 리포트 뷰. embed.FS 번들.
- AC:
  - [ ] 실험 생성→실행→리포트 열람 E2E 동작
  - [ ] 그래프 작성(에디터 또는 파일)
  - [ ] `make build` 단일 바이너리에 UI 임베드
- 의존성: blocked-by T15
- 메타: 추정 4d · P1 · labels: `wave-3` `area-ui` `P1` · AD-001/AD-002

### T18 — [ui] 뷰어 read-only 공유 리포트 화면
- 배경/목적: 디자이너·기획자가 설정 권한 없이 리포트만 소비.
- 변경 사항: share-token URL 진입 read-only 리포트 페이지(지표·타임라인·이슈 목록).
- AC:
  - [ ] 토큰 URL로 읽기 전용 리포트 표시
  - [ ] 설정/실행 컨트롤 비노출
  - [ ] 만료 토큰 거부 처리
- 의존성: blocked-by T16, T17
- 메타: 추정 1d · P1 · labels: `wave-3` `area-ui` `P1` · AD-013

### T19 — [infra] 분산 워커 코디네이션 (master-worker)
- 배경/목적: 대용량(goal #3). 동일 바이너리 `--role=master|worker`.
- 변경 사항: master↔worker gRPC, 실험 정의 push, 지표 stream/집계, 워커 등록·헬스.
- AC:
  - [ ] master가 N워커에 부하 분배 + 지표 집계
  - [ ] 워커 추가/이탈 graceful
  - [ ] 무상태 워커(상태는 코디네이터/스토어)
- 의존성: blocked-by T7, T15
- 메타: 추정 4d · P2 · labels: `wave-4` `area-infra` `P2` · AD-007

### T20 — [infra] 분산 저장소 (Postgres + TSDB + 큐)
- 배경/목적: 대용량 메트릭·분산 조정.
- 변경 사항: Postgres(설정/결과), TSDB(TimescaleDB/ClickHouse 메트릭), 큐(NATS/Redis fan-out). Store 인터페이스 분산 구현.
- AC:
  - [ ] 분산 모드에서 고빈도 메트릭 적재 무손실
  - [ ] 큐로 워커 fan-out
  - [ ] 로컬↔분산 Store 무코드변경 전환(설정)
- 의존성: blocked-by T14, T19
- 메타: 추정 3d · P2 · labels: `wave-4` `area-storage` `P2` · AD-010

### T21 — [infra] Capacity 부하 검증 (로컬 2k / 분산 10k+·50k+ RPS)
- 배경/목적: spec-analyze G2 목표 달성 확인. goal #3.
- 변경 사항: 벤치 시나리오, capacity 측정, 추종 오차·자원 사용 리포트.
- AC:
  - [ ] 로컬 단일노드 ~2,000 동시 달성
  - [ ] 분산 10,000+ 동시 / 50,000+ RPS 달성
  - [ ] 워커 추종 오차 <10% 리포트
- 의존성: blocked-by T19, T20
- 메타: 추정 2d · P2 · labels: `wave-4` `area-infra` `P2` · AD-007/G2

## 의존성

```
T1 → T2 → {T3, T6, T13, T14}
T3 → T4 → {T5, T7}
T6 → T7 → {T8, T9, T10, T11}
T11 → T12 → T16
T7,T11 → T15 → {T16, T17, T19}
T16,T17 → T18
T7,T15 → T19 → T20 → T21
T14 → T20
```

## 우선순위

- **P0 (로컬 MVP 임계경로)**: T1, T2, T3, T4, T6, T7, T9, T11, T15  — "로컬에서 실험 1건 실행→지표 수집" 최소 수직 슬라이스 + 안전가드
- **P1 (핵심 가치 fast-follow)**: T5, T8, T10, T12, T13, T14, T16, T17, T18
- **P2 (대용량 분산)**: T19, T20, T21
