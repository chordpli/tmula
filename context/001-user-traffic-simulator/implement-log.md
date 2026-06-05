# Implement Log: user-traffic-simulator

## 작업 목록
- [x] #1 [infra] 프로젝트 스캐폴딩 & 빌드/테스트 파이프라인 (source: GitHub issue #1)
- [x] #2 [feat] 코어 도메인 모델 정의
- [ ] #3 [feat] 그래프 정의 포맷 파서·검증
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

## 블로커
- (없음)
