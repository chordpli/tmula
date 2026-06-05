# Pipeline Status: user-traffic-simulator

## 현재 Phase
- **implement: P0(8) + P1(9) + 리뷰 개선 패스 완료** → **스택 머지 / P2 재개 게이트 (사용자)**

## 진행 이력 (요약)
- 기획: req-intake→product-brief→tech-spec→spec-analyze→linear-split (GitHub 이슈 28)
- implement #1(bootstrap) + P0/P1 stacked PR 17개 (#29~#45, 전부 -race + CI green)
- **review-improvements** PR #46 (base #18) — 17 PR 코드 리뷰 + gemini 유효 피드백 반영
  - 8개 병렬 리뷰 에이전트 → 유효 발견만 추출(노이즈 폐기)
  - 15 소스 수정 + 8 회귀 테스트. 핵심: 롤링 auto-kill, 토큰 PII 마스킹, per-API FirstSeen, threshold mutated 제외, Server.Shutdown drain, 원자적 store, UI 견고화
  - codegen(#44): 안 함 + drift-guard 골든 JSON 테스트로 대체
  - go.mod 새 의존성 0 · 전 패키지 -race green · vitest 7/7 · CI green

## 코딩 원칙 (사용자 표준, 전 이슈 적용)
- stdlib 우선(go.mod=sigs.k8s.io/yaml만) · idiomatic Go · SOLID/확장-개방 · 과추상화 회피

## Skip 이력
- worktree-plan SKIPPED (단일 트리 stacked PR)

## 링크
- GitHub: https://github.com/chordpli/tmula (이슈 28, **PR 18개 open stacked**: #29~#46)
- 스택: main ← #29 ← … ← #45(#18) ← #46(review)

## 남은 작업
- 스택 머지 (18 PR, 바닥부터 main으로 — hard gate)
- P2 분산 (#19~#21): 디스크 정리됨. gRPC는 go≥1.23 필요(현 1.22) → 도입 시 go.mod·CI 상향 필요

## 다음 결정 (사용자 게이트)
- (a) 스택 머지 / (b) P2 재개(의존성 허용) / (c) 멈춤·리뷰
