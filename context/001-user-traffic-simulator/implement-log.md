# Implement Log: user-traffic-simulator

## 코딩 원칙 (사용자 표준 — 2026-06-05, 전 이슈 적용)
- **고퍼스럽게(idiomatic Go)**: accept interfaces / return structs, 작은 인터페이스, error wrapping, zero-value 유용성, 표준 레이아웃.
- **의존성 최소**: stdlib 우선. 새 외부 Go 의존성 금지 (기존 `sigs.k8s.io/yaml`만 유지). #14 저장소도 SQLite 대신 stdlib(in-memory + JSON 파일).
- **SOLID + 확장 개방 / 과도한 추상화 회피**: 실제 확장 축이 있는 곳만 인터페이스(Adapter/LoadStrategy/Mutator 등). 구현 1개뿐인데 인터페이스 만들지 않음.

## 작업 목록
- [x] #1 [infra] 프로젝트 스캐폴딩 & 빌드/테스트 파이프라인 (source: GitHub issue #1)
- [x] #2 [feat] 코어 도메인 모델 정의
- [x] #3 [feat] 그래프 정의 포맷 파서·검증
- [ ] #4~#21 (linear-plan 참조)

## 진행 중
- (없음)

## 완료

- **#2** — 완료 2026-06-05, branch `feat/pli/2-domain-model` (base main, stacked PR)
  - AC: [x] 11 엔티티 구조체+검증 단위테스트 / [x] Edge.dependency JSON 라운드트립(`TestEdgeDependencyRoundTrip`) / [x] env_class prod-locked 플래그(`TestProdLockedFlagExists`)
  - 구현: `internal/domain/enums.go`(10 enum + Valid), `entities.go`(TargetEnv·APITemplate·ScenarioGraph·Node·Edge·CredentialPool·LoadProfile·Experiment·RunExecution·MetricSample·Finding·ReportShare + Validate), 테스트 3파일
  - 추가 검증: Credential.Secret은 `json:"-"`로 직렬화 제외(PII, `TestCredentialSecretNotSerialized`)
  - Evidence: `go vet` clean · `go build ./...` OK · `go test ./...` ok · `gofmt -l` clean

- **#1** — 완료 2026-06-05, commit `602e4a2` `chore: scaffold Go+React project with build pipeline (#1)` (pushed origin/main, 이슈 #1 closed)

  ### #1 AC (Acceptance Criteria)
  - [x] `make build`가 단일 바이너리 산출 (React 정적 자산 embed.FS 골격 포함 — placeholder index.html)
  - [x] `make test` / `make lint` 통과
  - [x] CI(GitHub Actions)가 push/PR에서 build+test 실행 (`.github/workflows/ci.yml` — go job + web job)

  ### 구현 내용
  - `go.mod` (module github.com/chordpli/tmula, go 1.22)
  - `cmd/engine/main.go` — 엔트리(--role local|master|worker, --addr, --version), HTTP 서버(/healthz + embed UI), graceful shutdown
  - `internal/domain/role.go` — Role 타입 + ParseRole (+ 테스트)
  - `internal/{engine,load,obs,safety,store}/doc.go` — 패키지 골격(책임 명시)
  - `internal/web/embed.go` — embed.FS로 UI 서빙 (+ 테스트), `internal/web/static/index.html` placeholder
  - `web/` — React + Vite + TS 골격 (package.json, vite.config, App.tsx 등), package-lock.json
  - `Makefile` — build/web-build/embed/test/vet/lint/run (policy §7 SSOT)
  - `.github/workflows/ci.yml` — go(build/vet/gofmt/test) + web(npm ci/build)
  - `README.md`, `.env.example`, `.gitignore`(tooling·빌드산출물 제외)

  ### 검증 출력 (Evidence)
  - `go vet ./...`: clean
  - `gofmt -l .`: clean (unformatted 0)
  - `make build`: `go build -ldflags "-X main.version=dev" -o bin/tmula ./cmd/engine` → 7,361,186 B
  - `make test`:
    ```
    ok  github.com/chordpli/tmula/cmd/engine
    ok  github.com/chordpli/tmula/internal/domain
    ok  github.com/chordpli/tmula/internal/web
    (engine/load/obs/safety/store: no test files — 골격)
    ```
  - web `npm run build`: vite v5.4.21 → 30 modules, dist/assets/index-*.js 143KB, built in 343ms
  - 바이너리 스모크: `--version`→`dev`; `GET /healthz`→`{"status":"ok","role":"local","version":"dev"}`; `GET /`→embed UI
  - 상태: 빌드 OK / 테스트 OK / AC ✓ / commit+push 대기 (사용자 OK 게이트)

- **#3** — 완료 2026-06-05, branch `feat/pli/3-graph-format` (base feat/pli/2-domain-model, stacked)
  - AC: [x] 유효 그래프 YAML/JSON 파싱 + 라운드트립 / [x] weight>1·의존 사이클 거부 / [x] 의존엣지 위상정렬(`TopoSortDependencies`)
  - 구현: `internal/scenario/scenario.go`(Parse JSON·YAML via sigs.k8s.io/yaml, MarshalJSON), `validate.go`(weight 범위·per-node 합 검증, Kahn 위상정렬로 의존 사이클 탐지), 테스트 9개
  - 의존성 추가: `sigs.k8s.io/yaml`(json 태그 존중 → 도메인 모델 단일 소스)
  - Evidence: `go vet` clean · `go build ./...` OK · `go test ./internal/scenario` ok · `gofmt -l` clean

- **#4** — 완료 2026-06-05, branch `feat/pli/4-graph-engine` (base feat/pli/3-graph-format, stacked)
  - AC: [x] 전이확률 분포 통계 일치(4000샘플 ~70%) / [x] 의존 선행 미충족 전이 0건(500런) / [x] 종료 조건 정상 종료
  - 구현: `internal/engine/walker.go` — `NewWalker`(seeded RNG), `Walk`(가중 랜덤 전이 + `canEnter` 의존엣지 불가침 검증, 종료/maxSteps 가드), 테스트 6개
  - Evidence: `go vet` clean · `go build ./...` OK · `go test ./internal/engine` PASS(6) · `gofmt -l` clean

- **#6** — 완료 2026-06-05, branch `feat/pli/6-rest-adapter` (base feat/pli/4-graph-engine, stacked)
  - AC: [x] REST GET/POST/PUT/DELETE status/latency/body 수집 / [x] payload 템플릿 변수 치환 / [x] 인터페이스 gRPC/WS 확장 가능(Adapter 인터페이스)
  - 구현: `internal/load/adapter.go` — `Adapter` 인터페이스, `Render`(text/template로 path·header·payload 치환, {{.token}}/{{.subject}} 노출, missingkey=error), `RESTAdapter`(net/http, latency 측정, 5xx는 응답·transport는 에러), 테스트 6개
  - Evidence: `go vet` clean · `go build ./...` OK · `go test ./internal/load` ok(httptest) · `gofmt -l` clean

- **#7** — 완료 2026-06-05, branch `feat/pli/7-vu-runtime` (base feat/pli/6-rest-adapter, stacked)
  - AC: [x] N명 동시 실행 + graceful 취소(ctx 취소 시 무송신) / [x] 각 유저 독립 인증 컨텍스트(distinct creds) / [x] 동시 스모크(50, goroutine-per-user로 2k+ 지원 — 실부하는 #21)
  - 구현: `internal/load/runtime.go` — `VirtualUser`/`StepResult`/`Runner`. `Run`이 유저당 goroutine으로 walker 순회 + 노드별 API 호출, ctx 취소 시 즉시 중단, 결과를 mutex-guarded 수집, node→template 해석
  - Evidence: `go vet` clean · `go build` OK · `go test ./internal/load -race` ok(레이스 없음) · `gofmt -l` clean

- **#9** — 완료 2026-06-05, branch `feat/pli/9-safety-guard` (base feat/pli/7-vu-runtime, stacked)
  - AC: [x] 화이트리스트 밖 송신 0(AllowHost) / [x] rate+concurrency cap 초과 차단 / [x] 수동 kill 즉시 중단 + 자동 kill opt-in(기본 비활성 — 포화 관측 보장)
  - 구현: `internal/safety/guard.go` — `Guard`(host 화이트리스트 매칭 *.wildcard, 토큰버킷 rate cap, concurrency 세마포어, 수동 `Kill`, `ReportOutcome` 기반 자동 임계 trip), `NewGuardForEnv`(prod-locked는 명시 unlock 필요 — 정책 §1), injectable clock, 테스트 7개
  - Evidence: `go vet` clean · `go build` OK · `go test ./internal/safety -race` ok · `gofmt -l` clean

- **#11** — 완료 2026-06-05, branch `feat/pli/11-obs-collector` (base feat/pli/9-safety-guard, stacked)
  - AC: [x] status/latency/error_class 기록 / [x] p50/95/99·error_rate·timeout 집계 정확 / [x] 응답 assertion 결과 수집(errorClass로 기록 → #12 분류)
  - 구현: `internal/obs/collector.go` — `Collector`(thread-safe Record/RecordSample), `Snapshot`→`Stats`(nearest-rank 백분위, error_rate, timeout, status 분포 복사). 4xx/5xx 또는 errorClass present = error, errorClass="timeout" = timeout. 테스트 5개
  - Evidence: `go vet` clean · `go build` OK · `go test ./internal/obs -race` ok · `gofmt -l` clean

- **#15** — 완료 2026-06-05, branch `feat/pli/15-control-plane` (base feat/pli/11-obs-collector, stacked) — **P0 임계경로 캡스톤**
  - AC: [x] 전 엔드포인트 동작+입력검증(lifecycle, 빈 spec→400) / [x] SSE 실시간 진행 스트림(`/runs/{id}/stream`) / [x] kill 엔드포인트 Safety Guard 연동(guard.Kill + ctx cancel)
  - 구현: `internal/api/server.go` — `Server`(in-memory 레지스트리), 엔드포인트 `POST /experiments`·`GET /experiments/{id}`·`POST /experiments/{id}/run`·`POST /runs/{id}/kill`·`GET /runs/{id}/report`·`GET /runs/{id}/stream`(SSE). run은 engine+runtime+safety+obs를 통합 실행. `cmd/engine/main.go`에 `/api` 마운트(바이너리 통합)
  - Evidence: `go vet` clean · `go build` OK · `go test ./internal/api -race` PASS(5) · 전 패키지 `-race` green · 바이너리 스모크(`/api/...` 404/400 JSON) · `gofmt -l` clean
  - 참고: SSE는 현재 주기적 스냅샷(완료 시 최종 프레임); per-request 라이브 메트릭은 runtime sink 도입 시 향상(후속). store는 in-memory(#14에서 영속화 교체)

- **#5** — 완료 2026-06-05, branch `feat/pli/5-deviation-mutation` (base feat/pli/15-control-plane, stacked) — **P1 시작**
  - AC: [x] 이탈% 분포 + 의존엣지 위반 0(500런) / [x] 변형이 4xx 유발(mutate→adapter→SUT 400) / [x] 변형 on/off 토글 + 강도(Rate, 빈 set no-op)
  - 구현: `internal/engine/deviation.go`(`DeviationPolicy`{Rate,Abandon,Explore}, `WalkWithDeviation` — 의존엣지 불변), walker DRY 리팩터(`eligible`/`weightedPick` 추출). `internal/load/mutate.go`(`Mutate` + `DefaultMutations` 슬라이스 — null/empty/huge/negative/type-swap). 테스트 7개
  - 원칙 적용: stdlib only · Mutation은 슬라이스(인터페이스 남발 회피) · 확장은 DefaultMutations append로
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/engine ./internal/load -race` ok · 전 패키지 green

- **#8** — 완료 2026-06-05, branch `feat/pli/8-load-strategy` (base feat/pli/5-deviation-mutation, stacked)
  - AC: [x] 가중 집중(graph weight + LoadProfile.TargetAPIID) / [x] ramp/spike 시계열 의도 형태 추종(정확 보간, ±10% 테스트) / [x] 전략 플러그인으로 절대 RPS 후속 추가 가능(`LoadStrategy` 인터페이스)
  - 구현: `internal/load/strategy.go` — `LoadStrategy` 인터페이스 + `Constant`/`Ramp`/`Spike`/`Soak` + `NewStrategy`(domain.LoadProfile→전략). 순수 함수(시간→목표 동시성), 테스트 7개
  - 원칙: 인터페이스는 *실제 확장 축*(다중 전략 + 미래 RPS)이라 정당. stdlib only
  - 참고: 런타임에 전략 적용(시간축 동시성 스케줄링)은 후속 wiring (runner 스케줄러 / #19)
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/load` ok · 전 패키지 green

- **#10** — 완료 2026-06-05, branch `feat/pli/10-auth-provider` (base feat/pli/8-load-strategy, stacked)
  - AC: [x] 토큰풀에서 유저별 자격증명 배분(라운드로빈) / [x] 부트스트랩 가입 트래픽 인원수만큼 선행(`Prewarm`) / [x] 자격증명 마스킹 저장(Credential.Secret json:"-")
  - 구현: `internal/auth/auth.go`(신규 패키지) — `Provider` 인터페이스, `PoolProvider`(사전 풀), `BootstrapSignupProvider`(SignupFunc 주입, 유저별 캐시, Prewarm 선행 단계), `NewProvider` 팩토리. 테스트 6개
  - 원칙: 관심사 분리(별도 패키지), SignupFunc 주입으로 transport 독립(테스트성), 인터페이스는 확장축(OAuth 등) 정당
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/auth -race` ok · 전 패키지 green

- **#12** — 완료 2026-06-05, branch `feat/pli/12-finding-classifier` (base feat/pli/10-auth-provider, stacked)
  - AC: [x] 4 카테고리 탐지(threshold/contract/mutation/availability) / [x] severity 부여 + evidence_ref(apiID) / [x] 3모드 집계 — Aggregator는 모드-무관, 준수/이탈/부하 결합은 리포트(#16) 책임
  - 구현: `internal/obs/finding.go` — `RequestObservation`(Mutated 컨텍스트 포함), `Aggregator.Classify(cfg)` → `[]domain.Finding`. mutation(변형 실패), contract(정상경로 5xx/assertion), availability(연속 실패 run≥N), threshold(error_rate/p95 초과). API별 그룹핑(엔드포인트 1개=Finding 1개). 테스트 8개
  - 원칙: stdlib only, 인터페이스 없이 구조체+메서드(과추상화 회피), 결정적 정렬
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/obs -race` ok · 전 패키지 green

- **#13** — 완료 2026-06-05, branch `feat/pli/13-pii-masking` (base feat/pli/12-finding-classifier, stacked)
  - AC: [x] 지정 필드(토큰/이메일/전화) 마스킹 / [x] 미지정 의심필드 기본 가림(deny-by-default 휴리스틱) / [x] 로그·리포트·저장 적용 — Masker가 단일 chokepoint(`MaskJSON`/`MaskValue`); 실제 호출 wiring은 #16 리포트·#14 저장 통합 시
  - 구현: `internal/mask/mask.go`(신규 패키지) — `Masker`(always/allow + 휴리스틱 substr: password/token/secret/email/phone/jwt/session/card...), `MaskJSON`(재귀 워크 중첩·배열), `MaskValue`, allowlist override. 테스트 7개
  - 원칙: 단일 책임 패키지, 인터페이스 없이 구조체(1구현), stdlib only, deny-by-default(정책 §5.3)
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/mask` ok · 전 패키지 green

- **#14** — 완료 2026-06-05, branch `feat/pli/14-local-store` (base feat/pli/13-pii-masking, stacked)
  - AC: [x] 실험/런/결과 CRUD + 조회 / [x] 메트릭 고빈도 쓰기 로컬 수용(slice append, -race) / [x] Store 인터페이스가 분산(Postgres+TSDB) 교체 가능
  - 구현: `internal/store/store.go` — `Store` 인터페이스, `MemStore`(RWMutex, ErrNotFound, 복사 반환), `SaveToFile`/`LoadFromFile`(JSON 스냅샷). 테스트 6개
  - **사용자 지시 준수: SQLite/cgo 의존성 없이 stdlib(in-memory + JSON 파일)** — go.mod 새 의존성 0
  - 원칙: Store 인터페이스는 확장축(local/distributed) 정당, 컴파일 타임 `var _ Store` 보증, stdlib only
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/store -race` ok · 전 패키지 green · go.mod=sigs.k8s.io/yaml만

- **#16** — 완료 2026-06-05, branch `feat/pli/16-report-share` (base feat/pli/14-local-store, stacked)
  - AC: [x] 지표 + findings 포함 리포트(execute가 Aggregator로 분류해 Report.Findings) / [x] share-token 발급 + 토큰 보유로만 read-only(`POST /runs/{id}/share`·`GET /reports/shared/{token}`, scope=viewer) / [x] 공유 리포트 PII 마스킹(`mask.MaskJSON`)
  - 구현: `internal/api/share.go`(신규 — crypto/rand opaque 토큰, ttl 만료, masked read-only), `server.go` 확장(findings 계산, Report.Findings, masker 필드, `runState.report` 헬퍼). 테스트 4개 + 기존 5개
  - 참고: 3모드(준수/이탈/부하) 명시 분리 비교는 후속(현재 단일 run stats+findings); per-request 라이브 메트릭도 후속
  - Evidence: `go vet`/`gofmt` clean · `go test ./internal/api -race` ok(9) · 전 패키지 green

- **#17** — 완료 2026-06-05, branch `feat/pli/17-react-ui` (base feat/pli/16-report-share, stacked)
  - AC: [x] 실험 생성→실행→리포트 열람 E2E(폼→createExperiment→startRun→SSE→getReport) / [x] 그래프 작성(JSON textarea 에디터) / [x] `make embed` 단일 바이너리에 UI 임베드(검증: / → React 번들, assets 200)
  - 구현: `web/src/api.ts`(순수 헬퍼 buildRunSpec/parseSSEData + fetch 래퍼), `App.tsx`(폼·실시간 SSE·findings 뷰), `api.test.ts`(vitest 5). package.json build=`tsc --noEmit && vite build`, test=`vitest run`. CI web job에 `npm test` 추가
  - 원칙: 런타임 의존성은 react/react-dom만(추가 0), 테스트는 dev 전용 vitest, 순수 로직 분리해 테스트성 확보
  - 빌드 아티팩트(static/assets, dist) gitignore — 커밋엔 placeholder index.html 유지(go-only CI 빌드 보호)
  - Evidence: vitest 5/5 · `tsc --noEmit` + `vite build` ok(148KB) · `make embed` → 바이너리 UI 서빙 스모크 ok · 전 Go 패키지 green

- **#18** — 완료 2026-06-05, branch `feat/pli/18-viewer` (base feat/pli/17-react-ui, stacked) — **P1 마지막**
  - AC: [x] 토큰 URL로 read-only 리포트(`?share=<token>` → Viewer) / [x] 설정/실행 컨트롤 비노출(Viewer는 ReportView만) / [x] 만료 토큰 거부(getSharedReport 410→메시지)
  - 구현: `web/src/ReportView.tsx`(공유 프레젠테이션 — operator+viewer 재사용 DRY), `Viewer.tsx`(share 리포트 fetch·read-only), `App.tsx`가 `shareTokenFromQuery`로 Viewer/Operator 분기, `api.ts`에 getSharedReport/shareTokenFromQuery. vitest 2개 추가
  - 원칙: ReportView 추출로 중복 제거(DRY), 런타임 의존성 추가 0, 순수 헬퍼 테스트
  - Evidence: vitest 7/7 · `tsc --noEmit` + `vite build` ok(149KB) · 전 Go 패키지 green

- **review-improvements** — 완료 2026-06-05, branch `refactor/pli/review-improvements` (base feat/pli/18-viewer) — **17 PR 코드 리뷰 + gemini 유효 피드백 반영**
  - 방식: 8개 병렬 리뷰 에이전트가 패키지별로 원칙 대조 + 각 PR gemini 리뷰 fetch → 유효한 설계/로직/보안만 추출(의존성 버전·sunset 배너·false positive 폐기)
  - 적용한 수정 (15 소스):
    - `safety`: **auto-kill 누적률→롤링 윈도우**(긴 런에서 안 터지던 버그) · parseHost 스킴 없는 호스트 · AllowHost 락 밖 파싱
    - `mask`: **pin/card 과잉마스킹→토큰 매칭**(shipping_address 등 보호) · passphrase/private/signature 등 누락 보강 · HTML 이스케이프 끔
    - `obs`: **FirstSeen API별** · **threshold가 mutated 제외** · Snapshot 정렬을 락 밖으로
    - `domain`: NaN weight 거부 · Shape 음수 거부 · bootstrap 빈 flowid 거부 · Credential.String() 시크릿 가림 · dead Multiplier 제거
    - `scenario`: NaN weight 거부 / `engine`: 음수 maxSteps 거부
    - `auth`: signup I/O를 락 밖으로(in-flight 메모이제이션) / `load`: 응답 body LimitReader · ramp 부호인식 반올림 · cancel break
    - `api`: **Server.Shutdown(런 goroutine drain)** · killRun 409 · body MaxBytesReader · SSE 25→250ms / `main`: shutdown에 drain
    - `store`: 원자적 SaveToFile(tmp+rename)
    - UI: SSE onerror 멈춤 가드 · Run 중복실행 disable · killRun .catch · 숫자입력 clamp · findings null 가드 · killRun 에러 표면화
  - **codegen(#44) 결정**: 하지 않음. 대신 `api/review_test.go`의 **drift-guard 골든 JSON 테스트**로 TS 타입 동기화 보장(에이전트 권고)
  - 회귀 테스트 8개 파일 추가(`*_review_test.go`) — 각 수정 잠금
  - Evidence: `go vet`/`gofmt` clean · `go test ./... -race` 전 패키지 green · vitest 7/7 · `vite build` ok · 바이너리 스모크 ok · go.mod 새 의존성 0

- **스택 머지** — 2026-06-06, P0+P1+review (18 커밋)을 main으로 fast-forward (59c0d3b). 이슈 #1~#18 closed, Epic E1~E6 closed, 로컬 브랜치 정리, main CI green.

- **P2 분산 (#19·#20·#21)** — 완료 2026-06-06, branch `feat/pli/p2-distributed` → PR #47 (base main) — **병렬 구현**
  - 방식: Phase0 토대(go 1.23+/grpc·pgx 의존성·CI) → **Phase1 3개 worktree 에이전트 병렬**(#19 cluster·#20 store·#21 bench) → Phase2 파일단위 통합+cmd 배선
  - #19 `internal/cluster`: gRPC master/worker. master가 가상유저를 워커에 분할(splitUsers)→server-streaming RunShard→결과를 한 Collector로 집계. 워커는 기존 load.Runner로 샤드 실행. 생성 protobuf 커밋(CI 코드젠 불요). bufconn 인-프로세스 테스트
  - #20 `internal/store/postgres.go`: PostgresStore(pgx/v5)가 기존 Store 인터페이스 2번째 구현(MemStore 불변). 멱등 Migrate, JSONB+쿼리컬럼. 통합테스트는 `TMULA_TEST_POSTGRES` 없으면 SKIP + no-DB 유닛테스트
  - #21 `internal/bench`: stdlib capacity 하니스(목표 동시성 대비 달성 RPS·tracking error·백분위) + 벤치마크(~15.6k RPS 측정)
  - cmd: `--role worker`가 gRPC cluster 서비스 서빙
  - 의존성 결정(사용자 허용): grpc v1.81→**go 1.25 필요** → go.mod·CI 1.25. buf(자체 컴파일러)로 코드젠
  - Evidence: `go vet`/`gofmt` clean · `go test ./... -race` 전 14 패키지 green · 바이너리 worker 기동 ok · **PR #47 CI(go 1.25) success**

## 블로커
- (없음)
