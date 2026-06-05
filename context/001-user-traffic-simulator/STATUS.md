# Pipeline Status: user-traffic-simulator

## 현재 Phase
- **implement — P0(8) + P1(9) 전부 완료** → **P2 결정 / 스택 머지 게이트 (사용자)**
- P2(#19~#21 분산)는 의존성 결정 필요 — 사용자 게이트

## 진행 이력 (요약)
- 기획 파이프라인: req-intake→product-brief→tech-spec→spec-analyze→linear-split (GitHub 이슈 28)
- implement #1 (main bootstrap) + **P0/P1 stacked PR 17개 (전부 -race + CI green)**:
  - P0: #2 도메인(#29) · #3 그래프포맷(#30) · #4 엔진(#31) · #6 REST(#32) · #7 런타임(#33) · #9 안전(#34) · #11 수집(#35) · #15 컨트롤플레인(#36)
  - P1: #5 이탈+변형(#37) · #8 부하전략(#38) · #10 Auth(#39) · #12 이슈판정(#40) · #13 PII마스킹(#41) · #14 저장소(#42) · #16 리포트+공유(#43) · #17 React UI(#44) · #18 뷰어(#45)
- 코딩 원칙 적용: stdlib 우선(go.mod=sigs.k8s.io/yaml만), idiomatic, SOLID/확장-개방, 과추상화 회피
- 12 Go 패키지 + React UI(vitest), 전 패키지 `-race` green, CI(go+web) green

## Skip 이력
- worktree-plan SKIPPED (단일 트리 stacked PR)

## 링크
- GitHub: https://github.com/chordpli/tmula (이슈 28, PR 17개 open stacked)
- 스택: main ← #29 ← … ← #45 (전부 미머지)

## 남은 작업 (P2 — 의존성 결정 필요)
- #19 분산 master-worker (gRPC 의존 vs stdlib HTTP 코디네이션)
- #20 분산 저장소 (Postgres+TSDB+큐 = 드라이버 의존 불가피, "의존성 없이"와 충돌)
- #21 capacity 부하 검증 (stdlib 가능)

## 다음 결정 (사용자 게이트)
- (a) 스택 머지 (17 PR 바닥부터 main으로) / (b) P2 stdlib-only / (c) P2 의존성 허용 / (d) 멈춤·리뷰
