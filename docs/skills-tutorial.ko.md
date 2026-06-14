# Claude Skill을 활용한 tmula 셋업

*[English](skills-tutorial.md) · 한국어*

데모용으로 만들어진 쇼핑몰 API에 부하를 가하는 전 과정을 오케스트레이터 `/tmula-up` 한 번으로 실행한 기록입니다. 명령과
출력은 모두 실제 실행 결과입니다. 스킬별 상세는 [skills-guide.ko.md](skills-guide.ko.md)에 있습니다.

## 파이프라인의 스킬

| 스킬 | 하는 일 | 실행되는 명령 |
|---|---|---|
| **tmula-scaffold** | 스펙에서 시나리오 기본 틀 생성 (+ 콘솔용 graph/templates) | `tmula init`, `to-console.sh` |
| **tmula-enrich** | 시나리오를 실행 가능·안전하게 구체화 (CLI 명령 없음, 파일 편집) | (없음) |
| **tmula-run** | 안전 게이트 → dry-run → 부하 실행 | `tmula run` |
| **tmula-triage** | finding 재현 · 베이스라인 게이트 | `tmula reproduce`, `tmula run --baseline-file` |
| **tmula-up** | 위 네 스킬을 순서대로 실행 | (위 스킬 호출) |

생성되는 JSON은 모두 `json/` 폴더에 저장됩니다(생성물이므로 gitignore): 시나리오·리포트·베이스라인, 그리고
웹 콘솔용 `graph.json`·`templates.json`·`source.json`.

---

## 0. 준비 (1회)

부하 대상으로 레포의 데모용으로 만들어진 쇼핑몰 API(`server/examples/sample-api`)를 사용합니다. 의도적으로 버그를 심어둔
서버이며, 부하를 가하면 장바구니·결제에서 문제가 발생합니다.

```bash
REPO=$(git rev-parse --show-toplevel); cd "$REPO"
make embed                                                   # bin/tmula 에 React 콘솔 임베드
make shop &                                                  # 데모 shop SUT (:9000, 부하 대상)
./bin/tmula --addr 127.0.0.1:8090 &                          # tmula 엔진 + 웹 콘솔
# 확인: shop :9000 200 · engine :8090 200
```

데모 API는 실제 서비스처럼 `GET /openapi.json`으로 자체 OpenAPI 스펙을 제공합니다(서버 URL은 상대 경로 `/`).
따라서 주소만 입력하면 scaffold 헬퍼가 `/openapi.json`을 찾아 스펙을 가져오고 타깃까지 결정합니다. 로컬
파일을 직접 넘길 필요가 없습니다. 스펙을 제공하지 않는 API라면 스펙 파일·HAR·액세스 로그를 입력합니다.

---

## 호출

```
/tmula-up localhost:9000
```

스킬 커맨드와 함께 API의 주소만 입력했습니다. tmula-up은 scaffold(스펙 자동 발견) → enrich → 안전 게이트 → run → triage 순으로
진행합니다. 아래는 그 6단계의 실제 결과입니다.

---

## [1/6] scaffold

헬퍼로 서버에서 스펙을 발견하고, `tmula init`으로 기본 틀을 만들어 `json/scenario.json`에 저장합니다. 웹
콘솔을 사용할 경우 콘솔 편집 필드용 JSON도 함께 생성합니다.

```bash
eval "$(.claude/skills/tmula-scaffold/scripts/fetch-openapi.sh http://localhost:9000)"
# → 발견: http://localhost:9000/openapi.json ·  SPEC=/tmp/...json  TARGET=http://localhost:9000
mkdir -p json
./bin/tmula init --from "$SPEC" --target "$TARGET" --out /tmp/up.yaml
python3 -c "import yaml,json; json.dump(yaml.safe_load(open('/tmp/up.yaml')), open('json/scenario.json','w'), indent=2)"
```

주소만 입력했으나 헬퍼가 `/openapi.json`을 찾았고, 서버가 상대 경로이므로 타깃을 `http://localhost:9000`으로
결정했습니다. **6스텝**. 경로 파라미터 없음, 인증 없음, 변경 요청 `addToCart`·`checkout` 포함. 아직
구체화하기 전의 기본 틀입니다.

**웹 콘솔(:8080) 산출물.** 콘솔의 직접 편집 필드는 compact 시나리오가 아니라 *graph*와 *templates*를
따로 받습니다. 동일한 `/api/import`로 그 형식 그대로 생성합니다(엔진이 실행 중이어야 합니다).

```bash
cp "$SPEC" json/source.json                                  # 콘솔 "임포트" 필드용 원문
./.claude/skills/tmula-scaffold/scripts/to-console.sh http://localhost:8090 "$SPEC" auto
# → wrote json/graph.json + json/templates.json  (Start node: 'browse' · Max steps: 6)
```

| 콘솔 필드 | 파일 |
|---|---|
| 시나리오 그래프 | `json/graph.json` |
| API 템플릿 | `json/templates.json` |
| OpenAPI/HAR/로그에서 가져오기 | `json/source.json` |

이 graph·templates는 콘솔의 임포트가 생성하는 것과 동일한 baseline입니다. 필드에 입력한 뒤 콘솔에서
직접 편집하는 작업이 CLI에서의 enrich에 해당합니다.

---

## [2/6] enrich (파일 편집, CLI 명령 없음)

경로 파라미터를 채우고, 변경 요청은 기본적으로 제거하되 확인된 샌드박스에 한해 명시적으로 허용하여
유지하며, 인증이 있으면 `{{.token}}` 헤더로 연결합니다. shop은 파라미터와 인증이 없으므로 핵심은 안전
판단입니다.

규칙에 따라 `json/scenario.json`을 다시 작성합니다.

- 경로 파라미터 없음 → 치환 없음. 인증 없음 → 연결 없음.
- 변경 요청 `cart`·`checkout`: 기본 제거 대상이나 "로컬 데모 샌드박스이고 결제 흐름이 테스트 대상"임을
  확인하여 유지(명시적 허용).
- 구조화: `checkout`에 `dependsOn: addToCart`, finding 기준값, `users: 30`.

```json
{
  "target": "http://localhost:9000",
  "flow": [
    { "id": "browse",    "request": "GET /browse" },
    { "id": "category",  "request": "GET /category" },
    { "id": "search",    "request": "GET /search" },
    { "id": "product",   "request": "GET /product" },
    { "id": "addToCart", "request": "POST /cart",     "body": "{\"productId\":\"p7\",\"qty\":1}" },
    { "id": "checkout",  "request": "POST /checkout", "body": "{\"total\":42}", "dependsOn": "addToCart" }
  ],
  "users": 30,
  "findings": { "errorRate": 0.05, "p95LatencyMs": 800, "availabilityStreak": 5 }
}
```

enrich에는 대응하는 CLI 명령이 없습니다. 시나리오 구체화는 규칙에 따른 파일 편집으로 이뤄집니다.
cart·checkout이 유지된 것은 샌드박스임을 확인했기 때문이며, 확인하지 않았다면 두 요청은 제거됩니다.

---

## [3/6] 안전 게이트 (생략 불가)

트래픽 발생 전에 타깃이 운영 환경이 아닌지, 변경 요청을 명시적으로 허용했는지 확인합니다.

통과: `localhost:9000`은 로컬 루프백 데모(비운영)이며, 변경 요청 `cart`·`checkout`은 허용된 상태입니다. 가드
훅도 루프백 타깃을 허용합니다.

---

## [4/6] run - 웹 엔진에서 dry-run → 부하

먼저 1명으로 dry-run, 그다음 본 부하를 실행합니다. `--engine`을 사용하므로 실행이 서버 측에서 이뤄지고
결과가 보존됩니다.

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 1  --timeout 30s --fail-on-findings
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 30 --timeout 40s --json > json/report.json
```

**dry-run (1명):**

```text
Run run-22 — completed · local
  requests=6  errors=1 (16.7%)  status: 200:5  500:1
```

이번 dry-run은 깨끗하지 않습니다. cart의 약 8% 확률 500이 1명·6요청에서도 한 번 발생했습니다. dry-run이
깨끗하다고 안전이 보장되는 것은 아니며, 그 역도 성립합니다. 확률적 버그는 낮은 확률로 저부하에서도
간헐적으로 발생합니다. (`--fail-on-findings`로 인해 종료 코드는 2입니다.)

**본 부하 (30명):**

```text
Run run-24 — completed · local
  requests=180  errors=14 (7.8%)
  status: 200:166, 500:4, 503:10
  findings:
    • [CRITICAL] contract  : addToCart 계약 위반 4건
    • [CRITICAL] contract  : checkout  계약 위반 10건
    • [WARNING]  threshold : error rate 0.08 (기준 0.05 초과)
```

부하 상태에서 더 많은 실패가 나타났습니다. `addToCart` 500, `checkout` 503. 콘솔의 흐름 맵에서 확인할 수
있습니다: `http://localhost:8090/?run=run-24`. CRITICAL이 둘이므로 triage로 넘어갑니다.

---

## [5/6] triage - 원인 분류 + 베이스라인 게이트

각 finding을 무부하 단독으로 재현하고(엔진이 보존한 run 필요), 판정 결과를 "증명이 아니라 하나의 신호"로
함께 보고합니다. 베이스라인 게이트는 `--fail-on-findings`와 함께 사용하지 않습니다.

```bash
./bin/tmula reproduce --engine http://localhost:8090 --run run-24 --finding contract/checkout  --attempts 10
./bin/tmula reproduce --engine http://localhost:8090 --run run-24 --finding contract/addToCart --attempts 10
```

```text
checkout  → Verdict: flaky          — 무부하 단독 1/10 재현
addToCart → Verdict: load-dependent  — 무부하 단독 0/10 재현
```

판정 결과는 증명이 아니라 하나의 신호입니다. addToCart가 `load-dependent`(0/10)로 분류됐으나, 코드상 cart의
500은 부하와 무관한 약 8% 확률 버그입니다. 10회 시도에서 8% 확률이 0회로 나올 수 있으며(dry-run에서는
1명에서도 발생했습니다), `--attempts`를 늘리면 functional로 수렴합니다. checkout의 503은 동시성에 기인하므로
`flaky`와 `load-dependent` 경계에서 변동합니다.

**베이스라인 게이트:**

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 30 --json > json/baseline.json
./bin/tmula run json/scenario.json --engine http://localhost:8090 --users 30 --baseline-file json/baseline.json
```

```text
Baseline gate vs run-26: 0 new · 0 resolved · 3 persisting · 0 suppressed
exit code: 0
```

이번에는 새 finding이 없어 종료 코드 0(통과)이며, 알려진 세 finding은 `persisting`입니다. 다만 확률적
타깃에서는 베이스라인 캡처 시점에 나타나지 않은 finding이 다음 실행에서 `new`로 분류될 수 있습니다(이 경우
종료 코드 3). 그럴 때는 대표성 있는 베이스라인을 캡처하거나 `--known-issues`로 묶습니다.

---

## [6/6] end-to-end 요약

| 단계 | 결과 |
|---|---|
| 소스 | `localhost:9000` (주소만 - 스펙 자동 발견) |
| scaffold | 타깃 `http://localhost:9000`, 6스텝 → `json/scenario.json` (+ 콘솔용 `graph.json`/`templates.json`/`source.json`) |
| enrich | cart/checkout 샌드박스 허용 · `checkout dependsOn addToCart` · findings · users=30 |
| 안전 게이트 | 통과 (로컬 루프백 비운영 + 쓰기 허용) |
| run (dry-run, 1명) | run-22 · 6요청 · 1 에러 (cart 500, 확률 버그가 1명에서 발생) |
| run (부하, 30명) | run-24 · 180요청 · 7.8% 에러 · `addToCart`·`checkout` CRITICAL |
| triage 재현 | checkout=flaky(1/10) · addToCart=load-dependent(0/10) - 판정 결과는 신호 |
| 베이스라인 게이트 | `0 new · 3 persisting` → 종료 코드 0 (통과) |

산출물: `json/scenario.json` · `json/report.json` · `json/baseline.json` · 콘솔용 `json/graph.json` ·
`json/templates.json` · `json/source.json` (모두 gitignore). 라이브 콘솔: `http://localhost:8090/?run=run-24`.

---

## 요약

1. scaffold는 기본 틀만 잡습니다. enrich로 구체화해야 의도대로 실행할 수 있습니다.
2. 실제 운영 환경에 영향을 미치는 것을 주의해야 하므로, 안전 설정을 기본값으로 둡니다. 위험한 요청은
   기본적으로 제거되며, 본인 샌드박스에 한해 직접 포함시킵니다.
3. dry-run이 안전을 보장하지는 않습니다. 실제 문제는 대부분 부하 상태에서 발생하지만, 낮은 확률로
   저부하에서도 버그가 간헐적으로 발생합니다.
4. reproduce는 원인을 구분합니다. 판정 결과는 증명이 아니라 하나의 신호이므로, 표본이 작으면
   `load-dependent`와 `flaky` 사이에서 변동합니다. 판정이 경계에 걸리면 `--attempts`를 늘립니다.
5. CI는 새로운 문제만 차단합니다. 베이스라인으로 회귀를 탐지하되, 확률적 타깃은 대표성 있는 베이스라인이나
   `--known-issues`로 안정화합니다.
6. 콘솔 산출물도 스킬이 생성합니다. scaffold의 `to-console.sh`가 `:8080` 콘솔의 세 필드(시나리오 그래프 /
   API 템플릿 / 임포트)에 입력할 JSON을 생성합니다.

각 스킬의 상세는 [skills-guide.ko.md](skills-guide.ko.md)에 있습니다.

---

> 명령과 출력은 실제 `/tmula-up` 세션에서 가져왔습니다. run id(run-22, run-24, run-26 …)와 수치는 실행마다
> 달라지며, 데모 버그가 확률적이므로 재현·게이트 결과도 매번 조금씩 다릅니다.
