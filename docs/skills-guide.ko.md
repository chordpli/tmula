# tmula 스킬 - 전체 가이드

*[English](skills-guide.md) · 한국어*

내 API를 부하 상황에 넣어보고 어디서 먼저 깨지는지 찾아내는 과정을, 다섯 개의
[Claude Code](https://docs.claude.com/en/docs/claude-code) 스킬로 처음부터 끝까지 돕는 안내서입니다.
간단한 요약은 [skills.md](skills.md)에 있고, 이 문서는 자세한 설명서입니다. 각 스킬이 무슨 일을 하고,
언제 쓰며, 어떤 순서로 동작하고, 결과가 어떻게 나오는지(실제로 돌려본 출력까지) 담았습니다.

---

## 전체 그림

```
  소스                   json/scenario.json                 실행               findings
 (spec/HAR/log) ─▶ tmula-scaffold ─▶ tmula-enrich ─▶ tmula-run ─▶ tmula-triage
                          │               │              │             │
                          └───────────────┴──── tmula-up (네 단계를 한 번에 진행) ────┘
```

서로 의존하지 않고 따로따로도 쓸 수 있는 **아톰 스킬 4개**와, 이들을 순서대로 엮어주는
**오케스트레이터 1개**로 이루어져 있습니다. 모두 `.claude/skills/` 아래에 있고, 이 레포를 Claude
Code로 열면 자동으로 인식됩니다.

쓸 때는 슬래시 이름(`/tmula-up`, `/tmula-scaffold` …)을 부르거나, 그냥 하고 싶은 걸 말로 해도 됩니다
("이 swagger로 부하 테스트해줘", "내 시나리오 실행되게 만들어줘", "이 finding 진짜야?").

스킬끼리는 서로를 직접 부르지 않고 **파일만 주고받습니다.** 덕분에 각자 독립적으로 동작하고,
`tmula-up`은 이 흐름을 자동으로 이어주는 편의 기능일 뿐입니다.

| 파일 | 만드는 곳 | 쓰는 곳 |
|---|---|---|
| `json/scenario.json` | scaffold(생성), enrich(수정) | enrich, run, up |
| `json/report.json` (`tmula run --json`) | run | triage (`--baseline-file`) |
| `./bin/tmula` | `go build -o ./bin/tmula ./server/cmd/tmula` | 전부 |

각 스킬은 입력을 ① 직접 받은 경로 → ② 정해진 파일 이름(`json/scenario.json` / `json/report.json`) → ③ 그것도
없으면 사용자에게 질문, 이 순서로 찾습니다. 그래서 다른 스킬이 먼저 돌았다고 가정하지 않습니다.

**안전 장치.** 시나리오를 실행하면 **실제 HTTP 요청**이 타깃으로 나갑니다. 그래서 `tmula-run`에 반드시
거쳐야 하는 안전 확인이 들어 있고(`tmula-up`도 이 확인을 그대로 한 번 더 거칩니다), 타깃이
**운영 환경이 아닌지** 확인하고, 기본은 **읽기 전용**이며, DELETE 같은 변경 요청은 사용자가 직접
켜야만 포함됩니다. 가져온 스펙·로그는 **신뢰할 수 없는 입력**으로 다룹니다. tmula 자체 기본값도 같은
방향입니다: `envClass=dev`, `allow`는 타깃 호스트로 한정(fail-closed),
`rateCap`은 `{maxRps:10000, maxConcurrency:1000}`. 여기에 **가드 훅**(`.claude/hooks/tmula-guard`,
`.claude/settings.json`에 등록)이 결정적으로 백업합니다 - 타깃이 루프백/사설망이 아니거나 시나리오
파일이 없는 `tmula run`은 **차단**합니다. 실제 샌드박스를 의도적으로 부하 줄 땐
`export TMULA_ALLOW_TARGET="host"` 또는 `.tmula-allow` 파일로 opt-in. (훅은 세션 시작 때 로드됩니다.)

---

## 1. tmula-scaffold - 소스에서 시나리오 만들기

**하는 일.** API 명세를 받아 *기본 골격*인 `json/scenario.json`을 만들고 타깃 URL을 정합니다. 입력은
**OpenAPI 3 / Swagger 2** 문서(URL이나 파일), **HAR** 캡처, **액세스 로그**, 또는 스펙을 노출하는
**API의 베이스 URL**(헬퍼가 `/openapi.json`·`/v3/api-docs` … 를 뒤지고 Springdoc `swagger-config`까지
따라가므로 `http://host`만으로도 됨) 중 하나입니다.

**쓸 때.** 스펙이나 로그는 있는데 시나리오는 아직 없을 때. *"내 swagger로 시나리오 만들어줘."* 이
단계는 요청을 보내지 않습니다.

**동작 순서.**
1. 바이너리를 한 번 빌드합니다. 모든 명령은 레포 루트에서 실행하거나,
   `REPO=$(git rev-parse --show-toplevel)`로 경로를 잡고 씁니다.
2. 입력을 로컬 스펙 파일과 기본 타깃으로 변환합니다. 헬퍼가 URL, HTML로 된 Swagger-UI 페이지,
   Springdoc `swagger-config`까지 알아서 따라갑니다:
   ```bash
   eval "$(./.claude/skills/tmula-scaffold/scripts/fetch-openapi.sh <url>)"   # → SPEC=… TARGET=…
   ```
3. 타깃을 확인합니다. 스펙의 서버가 **상대 경로**(`/api/v3`)거나 아예 없으면 `tmula init`이 에러를
   내므로, 타깃은 Swagger URL 자체의 주소(origin)에서 끌어옵니다.
4. 임포트: `./bin/tmula init --from "$SPEC" --target "$TARGET" --out /tmp/scenario.yaml`.
5. JSON으로 변환: `python3 -c "import yaml,json; json.dump(yaml.safe_load(open('/tmp/scenario.yaml')), open('json/scenario.json','w'), indent=2)"`.

**포맷별 차이.** OpenAPI/Swagger는 *사용자 여정에 가까운 순서*(로그인 → 탐색 → 조회 → 장바구니 →
결제)로 정렬된 선형 `flow`가 됩니다. 스펙에 적힌 순서 그대로가 아니고, 변경 요청도 중간에 섞여 있으니
손봐야 합니다. HAR은 캡처된 순서대로의 선형 `flow`, 액세스 로그는 실제 트래픽에서 전이 가중치와
도착률까지 학습한 **그래프형** 시나리오(`graph`+`templates`+`start`+`open`)가 됩니다.

**결과** (실제 출력, `examples/imports/shop.openapi.yaml`):

```json
{
  "target": "http://127.0.0.1:9100",
  "flow": [
    { "id": "browse",   "request": "GET /browse" },
    { "id": "category", "request": "GET /category" },
    { "id": "search",   "request": "GET /search" },
    { "id": "product",  "request": "GET /product" },
    { "id": "addToCart","request": "POST /cart",     "body": "{\"productId\":\"p7\",\"qty\":1}" },
    { "id": "checkout", "request": "POST /checkout", "body": "{\"total\":42}" }
  ]
}
```

아직 **다듬기 전 골격**입니다. 경로 파라미터가 `{id}` 그대로 남아 있고, 인증도 없으며, 위험한 변경
요청도 섞여 있습니다. 다음은 tmula-enrich.

**웹 콘솔(`:8080`)용:** scaffold는 콘솔 편집 필드용 JSON도 만들 수 있습니다 -
`to-console.sh <engine-url> <source> [format]`(원문을 떠 있는 엔진의 `/api/import`에 POST)을 실행하면
`json/graph.json`(→ *시나리오 그래프* 필드) + `json/templates.json`(→ *API 템플릿* 필드)을 쓰고
*Start node* / *Max steps*를 출력합니다. 원문은 `json/source.<ext>`로 저장돼 *임포트* 필드에 씁니다.

---

## 2. tmula-enrich - 골격을 실행 가능하고 안전하게

**하는 일.** 단순 임포트가 못 하는 판단을 더합니다. 경로 파라미터에 실제 값 채우기, 인증 헤더 연결,
**위험한 요청 걸러내기**, 그리고 흐름 다듬기(의존 관계 / 가중치 / finding 기준값).

**쓸 때.** 시나리오(scaffold 결과든, 직접 쓴 것이든, 붙여넣은 것이든)가 아직 그대로는 깔끔하게 또는
안전하게 안 돌 때. 이 단계도 요청을 보내지 않습니다.

**동작 순서.**
1. 시나리오를 읽습니다(선형 `flow`든 그래프 `templates`든 둘 다 `headers`를 지원).
2. **남아 있는 경로 파라미터를 전부 채웁니다** - `example` → `enum[0]` → 타입 기준(정수→`1`, uuid→고정값,
   문자열→`"string"`) 순으로 고릅니다. `GET /pet/{petId}` → `GET /pet/1`. 하나라도 `{...}`가 남으면
   조용히 404가 납니다.
3. **안전 필터**: `DELETE`와 데이터를 바꾸는 `POST`/`PUT`/`PATCH`는 기본적으로 빼고, 사용자가 직접
   허용할 때만 남깁니다. (Petstore v3 기준 11개가 제거 대상입니다.)
4. **인증 연결**(스펙에 보안 설정이 있을 때): 토큰은 Go 템플릿 변수 `{{.token}}`(점 포함)로만 노출되고,
   스텝이 이를 직접 참조해야 풀의 값이 들어갑니다:
   - API-key 헤더 → `"headers": { "api_key": "{{.token}}" }`
   - OAuth2 / bearer → `"headers": { "Authorization": "Bearer {{.token}}" }`

   여기에 채워 넣을 자리를 표시한 `auth` 풀을 추가합니다. `{{.token}}`은 언제나 풀에 적힌 고정 토큰이고,
   `login` 스텝이 토큰을 만들어주는 게 **아닙니다**.
5. 필요하면 `dependsOn`, `weight`, `deviationRate`, `findings` 기준값, `users`를 설정합니다.

**결과** (shop 시나리오는 인증이 없습니다. 인증이 필요한 API라면 `auth` 블록과 헤더가 추가됩니다):
경로 파라미터 채움, 위험 요청 제거, 필요 시 `findings` 기준값 추가. 무엇을 채우고, 연결하고, 뺐는지
사용자에게 그대로 알려줍니다. 다음은 tmula-run.

---

## 3. tmula-run - 시나리오를 안전하게 부하 실행

**하는 일.** 시나리오를 타깃에 실행하고 findings를 보고합니다. **실제 요청이 나가는 단계**라, 먼저
운영 환경 여부를 확인하고, 1명으로 가볍게 돌려본 뒤(dry-run), 본 부하를 줍니다.

**쓸 때.** 실행 가능한 `json/scenario.json`으로 실제 타깃에 부하를 줘보고 싶을 때.

**먼저 확인할 것 (세 가지 모두).** ① 타깃이 운영 환경이 아닌지 · ② 의도치 않은 변경 요청이 없는지(기본
읽기 전용) · ③ `--users 1`도 진짜 요청이 나간다는 점.

**동작 순서.**
1. 위 안전 확인을 통과합니다.
2. **dry-run(1명)** - tmula에는 요청을 안 보내는 검증 모드가 없어서, 이게 가장 가벼운 실행입니다:
   ```bash
   ./bin/tmula run json/scenario.json --users 1 --timeout 30s --fail-on-findings
   ```
   종료 코드만 보고 성공이라 판단하면 안 됩니다 - 평범한 실행은 100% 실패해도 0으로 끝납니다.
   `--fail-on-findings`나 `--fail-on-severity`를 붙여야 종료 코드가 의미를 갖습니다. 출력 요약은 항상
   직접 읽어보세요.
3. **본 부하** (closed 또는 open 모델):
   ```bash
   ./bin/tmula run json/scenario.json --users 50                 # closed: 동시 사용자 50명
   ./bin/tmula run json/scenario.json --open 278 --for 3600      # open: 초당 278명 도착
   ```
4. `requests / errors / p50·p95·p99 / 상태 코드 / findings`를 있는 그대로 보고합니다.

**웹 콘솔에서 실행하기(웹 환경).** in-process 대신 떠 있는 엔진을 가리키면, 실행이 서버 쪽에서 이뤄지고
결과가 **보존되어** 브라우저 콘솔에서 그 run에 붙어볼 수 있습니다:

```bash
make web                                                    # 1회: React 콘솔 빌드 + 임베드
./bin/tmula --addr :8080                                    # 엔진 + 콘솔 (그냥 실행하면 됨; serve 서브커맨드는 없음)
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 1   # dry-run, 서버 쪽 실행
#   → http://localhost:8080/?run=<run-id> 를 브라우저로 열기
```
(인증이 있는 시나리오는 자격증명이 원격 `--engine`으로 넘어가지 않으니, 그런 경우는 in-process로 돌립니다.)

**결과** (실제 출력, 내장 sample-api를 대상으로 서버 쪽 실행):

```text
# dry-run — 사용자 1명
run id   : run-2 · completed
requests : 6 · errors: 0 · status: {200:6}
findings : 0                       # 깨끗함: shop의 심어둔 버그는 부하가 있어야 터지므로 1명으로는 안 잡힘

# 본 부하 — 사용자 50명
run id   : run-4 · completed
requests : 300 · errors: 19 · status: {200:281, 404:1, 500:3, 503:15}
findings : 2
  • [CRITICAL] contract : addToCart 계약 위반 3건
  • [CRITICAL] contract : checkout 계약 위반 15건
```

dry-run은 멀쩡한데 부하를 주니 빨개진다 - 이게 정상이고, tmula가 잡아내려는 바로 그 신호입니다.
findings가 나오면 다음은 tmula-triage.

**종료 코드.** `0` 정상 · `1` 실행 오류 · `2` finding 발생(`--fail-on-findings`/`--fail-on-severity`와
함께) · `3` 베이스라인 대비 새 finding.

---

## 4. tmula-triage - 끝난 run을 해석하기

**하는 일.** run이 무언가를 찾아냈을 때, finding을 단독으로 재현해보고(기능 버그인지 부하 탓인지),
베이스라인과 비교하고, CI에 거는 것까지 도와줍니다.

**쓸 때.** run이 finding을 냈는데, 어느 게 진짜인지 / 회귀가 생긴 건지 / 부하 테스트를 CI 게이트로 만들지
판단하고 싶을 때.

**A. 재현(reproduce)** (작은 재생 요청이 나갑니다 - 먼저 run이 쓴 타깃이 운영 환경이 아닌지 확인).
떠 있는 엔진이 그 run을 들고 있어야 합니다(in-process run은 보존되지 않습니다):

```bash
./bin/tmula run json/scenario.json --engine http://localhost:8080 --users 50    # 엔진에서 실행 → run-N
./bin/tmula reproduce --engine http://localhost:8080 --run run-N --finding contract/checkout --attempts 5
```

판정은 셋 중 하나입니다. **functional**(단독으로도 매번 실패 → 진짜 버그), **load-dependent**(혼자선
안 터짐 → 동시성·포화 문제), **flaky**(가끔 터짐). 이 판정은 *확정이 아니라 단서*입니다(원래의
타이밍·동시성까지 재현하지는 못합니다).

**결과** (실제 출력, shop의 checkout finding 재현):

```text
Verdict: load-dependent — 무부하 단독 0/5 재현 → 동시성·포화 문제로 추정
```

→ 핸들러 코드보다 자원 한계를 들여다보라는 신호입니다. (cart finding은 가끔만 재현돼 flaky로 나왔고,
이럴 땐 `--attempts`를 올려 다시 봅니다.)

**B. 베이스라인 게이트** - 정상으로 확인된 run과 비교해 **새로 생긴** finding에만 실패:

```bash
./bin/tmula run json/scenario.json --json > json/report.json                 # 정상 상태를 베이스라인으로 저장
./bin/tmula run json/scenario.json --baseline-file json/report.json          # 새 finding에만 종료 코드 3
```

> 베이스라인 게이트에는 `--fail-on-findings`를 **같이 쓰지 마세요.** 모든 finding에 실패하는 게이트(코드
> 2)가 회귀 게이트(코드 3)보다 먼저 동작해서, "새 것만 본다"는 비교가 무시됩니다. 회귀만 보려면
> 베이스라인 플래그만, 모든 finding에 실패하려면 `--fail-on-findings`만 씁니다.

이미 알고 있는 이슈는 만료일을 반드시 적어 묻어둘 수 있습니다: `--known-issues known-issues.yaml`(각
항목에 `expires: YYYY-MM-DD`가 있고, 그 날짜가 지나면 다시 올라옵니다).

**C. CI** - 위 두 게이트 중 하나를 고릅니다. 레포에는 게이트를 돌리고 결과를 PR 코멘트로 갱신해주는
composite GitHub Action(`action.yml`)도 함께 들어 있습니다.

---

## 5. tmula-up - 전체 과정을 한 번에

**하는 일.** **scaffold → enrich → run → triage**를 중간중간 확인을 받아가며, 지금 가진 것에서 이어받아
끝까지 진행합니다.

**쓸 때.** *"이 URL로 처음부터 끝까지 해줘."* / *"tmula 한 방에 돌려줘."*

**어디서 시작할지 판단.** spec/HAR/log가 있으면 scaffold부터, 골격 시나리오면 enrich부터, 사용자가
"다 다듬었다"고 하는 시나리오는 한 번 더 검사한 뒤(남은 `{param}`과 변경 요청을 grep) run, 끝난 run이면
triage로 들어갑니다.

**동작 순서.** scaffold(타깃 확인) → enrich(채우고·연결하고·거름) → **안전 확인**(운영 환경 아닌지 +
어떤 요청이 나가는지, 건너뛸 수 없음) → run(dry-run 후 본 부하; 웹 콘솔은 `--engine`) → triage(finding이
있으면) → 전체 요약.

**결과.** 한 흐름으로 정리됩니다: 소스 → 타깃 → 시나리오 형태 → 안전하게 걸러낸 내역 → 부하 결과 →
finding 판정 → 다음에 할 일.

---

## 레퍼런스

### json/scenario.json 스키마

필수: `target`과, `flow`(선형) 또는 `graph`+`templates`+`start`(분기형, 액세스 로그 임포트의 결과) 중
하나. 선형 스텝의 필드: `id`, `request`(`"METHOD /path"`), `body`, `headers`(값에 `{{.token}}` /
`{{.subject}}` 사용 가능), `dependsOn`(절대 건너뛰지 않는 선행 단계), `weight`(다음 단계로 갈 확률, 기본
1). 최상위 선택 항목(기본값): `allow`(기본은 타깃 호스트, fail-closed) · `users`(closed 모델, 기본 20) ·
`open`+`maxSteps`(open 도착 모델) · `seed`(1) · `deviationRate`(0) · `auth`(파일 안에서는 pool만) ·
`findings`(`errorRate` 0.2 / `p95LatencyMs`는 생략 시 비활성 / `availabilityStreak` 5).

### 처음부터 끝까지 예시 (이 가이드를 쓰면서 실제로 돌린 명령 그대로)

```bash
REPO=$(git rev-parse --show-toplevel); cd "$REPO"
go build -o ./bin/tmula ./server/cmd/tmula
( cd server && go build -o /tmp/sample-api ./examples/sample-api )         # 안전한 로컬 타깃
SAMPLE_API_ADDR=127.0.0.1:9100 /tmp/sample-api &                           # 테스트 대상 서버

# scaffold
./bin/tmula init --from examples/imports/shop.openapi.yaml --target http://127.0.0.1:9100 --out /tmp/s.yaml
mkdir -p json
python3 -c "import yaml,json; json.dump(yaml.safe_load(open('/tmp/s.yaml')), open('json/scenario.json','w'), indent=2)"

# 웹 환경
make web && ./bin/tmula --addr :8090 &
./bin/tmula run json/scenario.json --engine http://127.0.0.1:8090 --users 1  --timeout 30s   # dry-run → run-2, 전부 200
./bin/tmula run json/scenario.json --engine http://127.0.0.1:8090 --users 50 --timeout 40s   # 부하   → run-4, findings 2
#   http://127.0.0.1:8090/?run=run-4 열기   ← 실시간 트래픽 흐름 맵 + findings
```

### 자주 막히는 곳

| 증상 | 원인 | 해결 |
|---|---|---|
| init이 *"could not determine a target URL"* | 서버가 상대 경로거나 없음 | Swagger URL 주소에서 타깃을 끌어와 `--target`으로 전달 |
| 경로에 `{param}`이 남고 404 | 경로 파라미터를 안 채움 | tmula-enrich로 모든 `{...}` 치환 |
| 보호된 엔드포인트가 401/403 | `{{.token}}` 헤더가 없거나 토큰 미입력 | `headers` 블록 연결 후 풀의 자리값 채우기 |
| `template: …` 에러 / 요청이 안 나감 | 점 없는 `{{token}}`을 씀 | `{{.token}}`이어야 함 |
| 에러가 났는데 종료 코드 0 | 그냥 run만 함 | `--fail-on-findings` / `--fail-on-severity` 추가, 요약 직접 확인 |
| 베이스라인 게이트가 알던 이슈에도 실패 | `--fail-on-findings`를 같이 씀 | 빼거나(코드 2가 코드 3을 가로챔), `--known-issues`로 묻어두기 |
| `reproduce`가 run을 못 찾음 | run이 떠 있는 `--engine`에서 안 돌았음 | `--engine`으로 다시 실행한 뒤 reproduce |
| `/` 콘솔이 placeholder | UI 없이 빌드된 바이너리 | `make web`(또는 `make embed`)로 React 콘솔 임베드 |
| `import yaml` 실패 | PyYAML 미설치 | `pip install pyyaml`, 또는 `scenario.yaml` 그대로 사용(tmula는 둘 다 읽음) |
