# Pipeline Status: user-traffic-simulator

## 현재 Phase
- **implement 진행 중 — P0 임계경로 8개 완료** → **P1 진행 / 스택 머지 / 멈춤 게이트**
- autonomous 스코프(P0 path) 완료. 다음 결정은 사용자 게이트.

## 진행 이력 (요약)
- req-intake ✓ · product-brief ✓ · tech-spec ✓ (AD 13) · spec-analyze ✓ · linear-split ✓ (GitHub 이슈 28)
- git init + main 확립
- implement #1 ✓ (main bootstrap, CI green, 이슈 closed)
- **P0 임계경로 stacked PR (전부 build/vet/test/-race + CI green):**
  - #2 도메인모델 → PR #29 (base main)
  - #3 그래프포맷 파서 → PR #30
  - #4 그래프 실행엔진 → PR #31
  - #6 REST 어댑터 → PR #32
  - #7 가상유저 런타임 → PR #33
  - #9 Safety Guard → PR #34
  - #11 Observation Collector → PR #35
  - #15 Control Plane API (+바이너리 통합) → PR #36
- 8개 패키지 + cmd, 전 패키지 `-race` green, CI(go+web) 전부 success

## Skip 이력
- worktree-plan SKIPPED (단일 트리 stacked PR로 진행)

## 링크
- GitHub: https://github.com/chordpli/tmula
- 스택: main ← #29 ← #30 ← #31 ← #32 ← #33 ← #34 ← #35 ← #36 (전부 open, 미머지)
- 빌드: 단일 바이너리 `bin/tmula` (engine+UI+/api 통합)

## 남은 작업
- P1: #5(이탈+변형) #8(부하전략) #10(Auth) #12(이슈판정) #13(PII마스킹) #14(저장소) #16(리포트+공유) #17(React UI) #18(뷰어)
- P2: #19(분산) #20(분산저장) #21(capacity검증)

## 다음 결정 (사용자 게이트)
- (a) P1 계속 auto / (b) P0 스택 머지(hard gate) / (c) 멈춤·리뷰
