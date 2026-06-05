# Spec Analyze Report: user-traffic-simulator

> Generated: 2026-06-05 (READ-ONLY — 어떤 아티팩트도 수정하지 않음)
> Scope: 3 artifacts present / 5 absent

## Inventory

| Artifact | Status | Lines | Notes |
|---|---|---|---|
| 00-requirements.md | present | 240 | Q카드 10 (Q-001~003 P0 accepted, Q-004~010 status=pending) |
| 01-brief.md | present | 59 | NEEDS CLARIFICATION 마커 3 (L42/47/50, 평면 포맷) |
| 02-tech-spec.md | present | 174 | AD카드 12, 전부 accepted/user-overridden (pending 0) |
| 03-linear-plan.md | (absent) | — | 관련 짝 SKIP |
| 04-worktree-plan.md | (absent) | — | 관련 짝 SKIP |
| 05-qa-plan.md | (absent) | — | 관련 짝 SKIP |
| 06-pr-review-pack.md | (absent) | — | 관련 짝 SKIP |
| implement-log.md | (absent) | — | 관련 짝 SKIP |

## 일관성

- **requirements ↔ brief** [PASS] — 범위 6항목(`00-requirements.md` `## 범위`)이 brief 목표·성공지표(`01-brief.md` `## 목표`/`## 성공 지표`)에 모두 반영. 대상 3군(개발자/기획자/디자이너) ↔ brief 목표 2(비개발자 셀프서비스) 일치.
- **requirements ↔ tech-spec** [PASS] — 범위 6항목 전부 아키텍처에 매핑: 가상유저수→`Experiment.params`, API/payload→`ApiTemplate`, 행동→`ScenarioGraph`+Virtual User Runtime, 이탈→AD-005, 부하집중→`LoadProfile`/AD-006, 수집·리포트→Collector/Aggregator. 비범위(의존성 무시 건너뛰기)는 AD-005 "의존엣지 불가침"으로 *준수*.
- **brief ↔ tech-spec** [PASS] — brief 마커 3개 모두 tech-spec에서 처리(아래 §누락 G2 제외): kill switch(`01-brief.md:42`)→AD-008(`02-tech-spec.md:139`), PII(`01-brief.md:50`)→AD-011(`02-tech-spec.md:160`). brief 의존성 "기술 스택 tech-spec에서 확정"→AD-002(Go-only) 정상 추적.
- **brief ↔ qa-plan** [SKIP] — `05-qa-plan.md` 부재.
- **tech-spec ↔ linear-plan** [SKIP] — `03-linear-plan.md` 부재.
- **tech-spec ↔ worktree-plan** [SKIP] — `04-worktree-plan.md` 부재.
- 나머지 짝(linear↔qa, requirements↔pr-pack, implement-log↔linear) [SKIP] — 대상 부재.

## 누락

| # | severity | 위치 | 누락 항목 | 인용 |
|---|---|---|---|---|
| G1 | MEDIUM | `02-tech-spec.md` `## 인터페이스`/`## 데이터 모델` | **뷰어 read-only 공유 리포트 권한 모델 미정의**. brief 유저플로우 "뷰어=공유 리포트 URL 읽기 권한"(`01-brief.md` `## 유저 플로우`)과 AD-001 WHAT "뷰어=공유 리포트 URL 열람"은 있으나, share-token/permission 엔티티·엔드포인트 미모델 (`GET /runs/{id}/report`만 존재, 권한 구분 없음) | `01-brief.md:유저플로우` vs `02-tech-spec.md:인터페이스` |
| G2 | MEDIUM | `02-tech-spec.md` AD-007 | **정량 capacity target 부재**. brief 마커2(피크 동시유저/RPS 수치, `01-brief.md:47`)는 분산(C) 선택으로 "단일노드 충분성" 질문은 해소됐으나, tech-spec은 정성 범위("수천/수만+")만 명시 — QA·부하 검증용 *수치 목표* 없음 → qa-plan에 이월 필요 | `01-brief.md:47` vs `02-tech-spec.md:AD-007` |
| G3 | LOW | `02-tech-spec.md` `## 데이터 모델` | AD-003 Steelman의 비개발자 완화책(템플릿 갤러리/비주얼 그래프 에디터)이 데이터모델/인터페이스에 미반영 (목표 2 셀프서비스의 핵심 UX) | `02-tech-spec.md:AD-003` |
| G4 | LOW | 정책 §5.3 / AD-011 | 마스킹 *도구* TODO(외부 정책 의존). v1 자체 마스킹으로 우회 — 문서화됨, blocker 아님 | `02-tech-spec.md:165` |

## 모순

| # | severity | 충돌 | 인용 A | 인용 B |
|---|---|---|---|---|
| C1 | LOW | Q-008 권장(B 단일노드) vs 채택(C 분산) — 단 tech-spec이 `user-overridden`으로 *명시* → documented override (silent 아님) | `00-requirements.md` Q-008 "권장 B" | `02-tech-spec.md` AD-007 "user-overridden (D4=C)" |
| C2 | LOW | Q-001 문구 "독립 웹 도구" vs "로컬 실행 도구" — tech-spec이 "정밀화"로 명시 → traceable drift | `00-requirements.md` Q-001 status "독립 웹 도구" | `02-tech-spec.md` AD-001 "user-overridden ... 정밀화" |
| S1 | MEDIUM | **stale status 필드** — `00-requirements.md`의 Q-004~Q-010 status가 여전히 `pending`이나, 실제 결정은 `02-tech-spec.md` AD-004~AD-011에 `accepted/user-overridden`로 존재. 동일 결정의 status가 두 문서에서 불일치 | `00-requirements.md` Q-004~Q-010 "status: pending" | `02-tech-spec.md` AD-004~AD-011 "accepted/user-overridden" |

> C1·C2는 tech-spec이 override/정밀화를 *명시*해 추적 가능 → 진짜 모순 아닌 documented drift (LOW). S1은 추적성 저하라 MEDIUM.

## P0 게이트

next phase: `linear-split` (또는 `implement`)

> **P0 잔여 0건 — downstream phase 진입 가능.**

| 구분 | 카드 | 상태 | 비고 |
|---|---|---|---|
| P0 | Q-001, Q-002, Q-003 (`00-requirements.md`) | **accepted** | 차단 없음 |
| P1 (요구사항 표기) | Q-004~Q-009 | requirements=pending **but** tech-spec AD-004~AD-009/011=resolved | 실질 해소 (S1 stale 필드만 잔존) |
| P2 | Q-010 (리포트 형태) | requirements=pending, 분산 리포트는 AD 본문에 반영 | qa-plan에서 확정 가능 |
| AD (tech-spec) | AD-001~AD-012 | **0 pending** | 차단 없음 |
| 평면 마커 | `01-brief.md` L42/47/50 | tech-spec AD-008/AD-007/AD-011에서 해소 (G2 수치 제외) | **카드화 권고**: brief 마커를 Q카드 포맷으로 올리면 추적 강화 |

결론: **P0 blocker 0 → linear-split / implement 진입 ready.** 단 아래 권고의 MEDIUM 3건(G1/G2/S1)을 진입 전 정리하면 downstream 재작업이 줄어듦.

## 권고

> P0 잔여 0건이므로 *진행 차단 없음*. 아래는 품질 향상 권고 (사용자 결정 — 본 스킬 READ-ONLY).

| # | 항목 | 옵션 A | 옵션 B | 옵션 C | Implications |
|---|---|---|---|---|---|
| 1 | G1 뷰어 권한 모델 | tech-spec에 share-token + 권한 엔티티 추가 (`tech-spec` 재호출) | linear-split에서 "뷰어 권한" 이슈로 분리해 구현 단계 결정 | v1 비범위로 명시(단일 권한, 공유=URL 보유=열람) | A: 설계 보강 / B: 결정 이연 / C: 범위 축소·명시 |
| 2 | G2 capacity target | 지금 수치 확정(예: 분산 10k 동시/50k RPS) 후 tech-spec/qa 반영 | qa-plan 단계에서 부하 목표로 확정 | 정성 목표 유지(수치 미고정) | A: 명확한 검증 기준 / B: 자연 이연 / C: QA 합격기준 모호 |
| 3 | S1 stale status | requirements Q-004~010 status를 tech-spec 결정으로 동기화 | STATUS.md에 "tech-spec이 P1 authoritative" 1줄로 갈음 | 그대로 둠(tech-spec override 명시로 충분) | A: 완전 추적 / B: 경량 표기 / C: 무조치(LOW 수용) |

가장 시급한 순: **G2(capacity target) → G1(뷰어 권한) → S1(stale status)**. 셋 다 MEDIUM, 진입 차단은 아님.

본 리포트는 READ-ONLY입니다. 실제 수정은 적합 스킬(`tech-spec`/`req-intake`/`qa-plan`)로 진행하세요.
