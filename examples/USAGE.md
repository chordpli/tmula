# tmula 사용법 — 0 to 100

tmula를 처음부터 끝까지 단계별로 쓰는 법. 각 단계에 복붙 가능한 예시가 있습니다.
빠른 한 방 데모는 [`README.md`](README.md), 이 문서는 그 다음의 **심화·실전 가이드**입니다.

> 표기: 아래 `$API`는 `http://127.0.0.1:8080/api` 를 가리킵니다.
> ```bash
> API=http://127.0.0.1:8080/api
> ```

---

## 한눈에 — tmula가 하는 일

내가 정의한 **행동 그래프**를 따라 가상 유저를 실제 사람처럼 움직이게 해서,
**진짜 유저를 모으지 않고** 내 API의 문제를 찾습니다. 결과는 4종 finding으로 분류됩니다.

| finding | 심각도 | 의미 |
|---|---|---|
| `contract` | CRITICAL | 정상 경로인데 5xx/계약 위반 — 개발자가 놓친 버그 |
| `availability` | CRITICAL | 한 API에서 연속 실패 — 포화/다운 |
| `threshold` | WARNING | 전체 에러율/p95 지연이 한계 초과 |
| `mutation` | WARNING | 변형된 입력이 에러를 유발 |

---

## Step 0 — 빌드 & 3가지 역할

```bash
# 요구사항: Go 1.25+ (웹 UI까지 빌드하려면 Node 20+)
go build -o ./bin/tmula ./cmd/engine
./bin/tmula --version
```

바이너리 하나가 세 역할을 합니다.

```bash
./bin/tmula --role local  --addr :8080                                  # 엔진 + API + 임베드된 웹 UI (UI는 make web/embed로 빌드)
./bin/tmula --role worker --addr :9101                                  # 분산용 워커 (gRPC)
./bin/tmula --role local  --addr :8080 --workers 127.0.0.1:9101,127.0.0.1:9102  # 워커 풀을 둔 마스터
```

상태 확인: `GET http://localhost:8080/healthz`

---

## Step 1 — 가장 쉬운 길: `tmula run` CLI

curl/jq/별도 서버 없이 한 줄로. 바이너리가 in-process 엔진을 띄우고 실험을 돌린 뒤 findings를 출력합니다.

```bash
# 단일 엔드포인트
./bin/tmula run --target http://127.0.0.1:9000 --get /browse --users 30

# 압축 시나리오 파일 한 장 (examples/shop/scenario.yaml 참고)
./bin/tmula run examples/shop/scenario.yaml --users 80

# 유기적(도착률) 부하 — 열린 모델
./bin/tmula run examples/shop/scenario.yaml --open 200 --for 30
```

출력 예:
```
Run run-2 — completed · local
  requests=320  errors=80 (25.0%)  p50=7ms p95=26ms p99=181ms max=182ms
  status: 200:240 500:12 503:68

Findings (4):
  • [CRITICAL] contract: 68 contract violation(s) on checkout (unexpected error on the happy path) [checkout]
  • [CRITICAL] availability: 53 consecutive failures on checkout (saturation or downtime) [checkout]
  • [WARNING] threshold: error rate 0.25 exceeded threshold 0.20
```

플래그: `--users N`, `--open RATE --for SECONDS [--ramp-to PEAK]`, `--seed N`,
`--json`(원시 리포트), `--engine http://host:8080`(실행 중 엔진에 보냄), `--timeout`.
시나리오 파일 포맷은 Step 2~7에서, 직접 REST를 쓰려면 Step 4를 보세요.

**CI 게이트** — findings가 있으면 비정상 종료시켜 파이프라인을 막을 수 있습니다:
```bash
tmula run scenario.yaml --users 80 --fail-on-findings           # findings 있으면 exit 2
tmula run scenario.yaml --users 80 --fail-on-severity critical  # critical 만 게이트
```
종료코드: **0 정상 · 1 에러 · 2 findings**. 실패/킬된 런은 findings 무관 비정상 종료.

### `tmula bench` — 용량(capacity) 한 줄 측정

findings 분류 대신 **숫자 하나**(이 동시성에서 얼마나 버티나)가 필요할 때. 컨트롤 플레인을
거치지 않고 in-process로 바로 돌려 달성 RPS·총요청·지속시간·에러율·트래킹오차·p50/p95/p99를 찍습니다.
플래그 레이아웃은 `tmula run`과 동일(시나리오 파일 또는 `--target`+`--get`/`--post`):
```bash
# 시나리오 파일을 80 동시성으로
tmula bench examples/shop/scenario.yaml --users 80

# 단일 엔드포인트
tmula bench --target http://127.0.0.1:9000 --get /browse --users 50
tmula bench --target http://127.0.0.1:9000 --post /cart  --users 50 --json
```
출력 예:
```
Bench — target concurrency: 80
  achieved RPS:     1770.5
  total requests:   160
  duration:         90 ms
  error rate:       4.00%
  tracking error:   0.00%
  latency p50/p95/p99: 9 ms / 13 ms / 18 ms
```
플래그: `--users N`(기본 50), `--max-steps N`, `--timeout`(기본 10s), `--seed N`, `--json`.
`run`과 달리 findings를 만들지 않으므로 `--engine`/CI 게이트는 없습니다 — 순수 용량 측정용입니다.

### 시나리오 파일 포맷 (한 장으로 graph+templates+target)
```yaml
target: http://localhost:9000
allow: [127.0.0.1]            # 생략 시 target 호스트로 자동
flow:                        # 순서대로 실행, 연속 스텝은 자동 연결
  - id: browse
    request: GET /browse
  - id: cart
    request: POST /cart
    body: '{"qty":1}'
  - id: checkout
    request: POST /checkout
    body: '{"total":42}'
    headers:                 # 선택: 스텝별 정적 헤더 ({{.token}}·{{.subject}} 템플릿 가능)
      Authorization: "Bearer {{.token}}"
    dependsOn: cart          # 이 간선을 의존성(절대 스킵 안 됨)으로 표시
users: 80                    # 선택: closed 모델 유저 수 (기본 20)
auth:                        # 선택: 있으면 런을 인증함 (없으면 익명)
  strategy: pool             #   기본 pool — 미리 만든 테스트 계정 주입 (bootstrap-signup 은 후속)
  users:                     #   유저 i 에게 i 번째 자격증명 (엔트리보다 유저가 많으면 wrap)
    - { subject: alice, token: jwt-aaa }   # token → {{.token}}, subject → {{.subject}}
    - { subject: bob,   token: jwt-bbb }
open:                        # 선택: 있으면 열린 모델로 전환
  rate: 200                  #   또는 from/to + rampSeconds 로 ramp
  forSeconds: 30
  thinkMs: [200, 800]
segments:                    # 선택: 페르소나 믹스 (open 전용)
  - { name: browser, weight: 0.7, start: browse }
  - { name: buyer,   weight: 0.3, start: cart }
```
> **인증(`auth`)**: `token` 은 자격증명의 비밀값으로, `{{.token}}` 템플릿(예: `Authorization: "Bearer {{.token}}"`)에 채워집니다. closed 모델은 유저 인덱스로, open 모델은 세션(도착) 인덱스로 자격증명을 배정합니다 — 둘 다 엔트리 수를 넘으면 순환합니다. 비밀값은 절대 직렬화·영속화되지 않으므로(마스킹), `tmula run` 은 이 경우 **인-프로세스**로 실행합니다(원격 `--engine` 에는 비밀값이 전송될 수 없어 미지원).

### 기존 API에서 시작: `tmula init`

빈 시나리오를 손으로 쓰지 말고, **OpenAPI 스펙이나 HAR 녹화**에서 초벌 시나리오를 생성하세요.

```bash
tmula init --from openapi.yaml --out scenario.yaml   # OpenAPI(서버/경로/예시 바디)에서
tmula init --from session.har  --out scenario.yaml   # 브라우저 HAR 녹화(요청 순서 그대로)에서
# 그 다음 스텝 순서/바디만 다듬고:
tmula run scenario.yaml --users 50
```
> 생성물은 **시작점**입니다 — 경로 파라미터(`{id}`)·바디·스텝 순서를 검토하세요. 형식은 `--format openapi|har`로 강제할 수 있고, `--target`으로 대상 URL을 덮어쓸 수 있습니다.

### 결과를 HTML로 보기 / 두 런 비교

```bash
# 한 런을 공유용 단독 HTML 페이지로
curl -s "$API/runs/$RUN/report.html" > report.html

# 두 런 비교 (회귀 감지: 새로 생긴 / 해결된 / 지속되는 findings + 통계 델타)
curl -s "$API/runs/compare?a=$RUN_BEFORE&b=$RUN_AFTER" > compare.html
```

## Step 1b — 풀스택 데모 한 방

```bash
./examples/run-demo.sh          # 기본 60 유저
./examples/run-demo.sh 100      # 100 유저
```

샘플 "쇼핑몰" API(`:9000`) + 엔진(`:8080`)을 띄우고 `browse → search/category → product → cart → checkout`
분기 시나리오를 돌린 뒤 리포트를 출력합니다. (요구: `go`, `jq`, `curl`)

```
------------- FINDINGS -------------
  • [CRITICAL] contract: 6 contract violation(s) on product (unexpected 404 on the happy path)
  • [CRITICAL] contract: 8 contract violation(s) on cart (unexpected error on the happy path)
  • [CRITICAL] contract: 90 contract violation(s) on checkout (unexpected error on the happy path)
  • [CRITICAL] availability: 53 consecutive failures on checkout (saturation or downtime)
  • [WARNING]  threshold: error rate 0.24 exceeded threshold 0.20
```

샘플 API의 `product`는 ~2% 404, `cart`는 ~8% 500, `checkout`은 부하를 받으면 포화되지만 부하가 줄면 회복됩니다 — tmula가 **실제 유저가 겪기 전에** 그걸 잡습니다.

---

## Step 2 — 핵심 입력 3가지

tmula에 주는 건 결국 이 셋입니다.

**① templates — 각 노드가 호출하는 실제 HTTP 요청**
```json
{
  "t_browse":   { "method": "GET",  "path": "/browse" },
  "t_product":  { "method": "GET",  "path": "/product" },
  "t_cart":     { "method": "POST", "path": "/cart",     "payloadTemplate": "{\"productId\":\"p7\",\"qty\":1}" },
  "t_checkout": { "method": "POST", "path": "/checkout", "payloadTemplate": "{\"total\":42}" }
}
```

**② graph — 유저가 노드 사이를 어떻게 이동하는지 (가중치 + 의존성)**
```json
{
  "id": "shop",
  "nodes": [
    { "id": "browse",   "apiTemplateId": "t_browse" },
    { "id": "product",  "apiTemplateId": "t_product" },
    { "id": "cart",     "apiTemplateId": "t_cart" },
    { "id": "checkout", "apiTemplateId": "t_checkout" },
    { "id": "exit" }
  ],
  "edges": [
    { "from": "browse",  "to": "product",  "weight": 0.7 },
    { "from": "browse",  "to": "exit",     "weight": 0.3 },
    { "from": "product", "to": "cart",     "weight": 0.45 },
    { "from": "product", "to": "exit",     "weight": 0.55 },
    { "from": "cart",    "to": "checkout", "weight": 0.6, "dependency": true },
    { "from": "cart",    "to": "exit",     "weight": 0.4 }
  ]
}
```
> `weight` = 그 간선으로 갈 확률, `dependency: true` = **절대 건너뛸 수 없는 순서**(cart 없이 checkout 불가).
> `exit` 같은 template 없는 노드는 이탈 지점 — 유저 세션이 자연스럽게 종료됩니다.
> 유저가 이탈(deviate)해도 의존성 간선은 깨지지 않습니다.

**③ targetEnv — 대상 주소 + 안전장치**
```json
{
  "baseUrl": "http://127.0.0.1:9000",
  "allowlist": ["127.0.0.1"],
  "rateCap": { "maxRps": 20000, "maxConcurrency": 1000 },
  "envClass": "dev"
}
```
> 안전장치: `allowlist`에 없는 호스트는 차단. `envClass`는 `dev`/`staging`만 허용(`prod`는 잠김).
> `rateCap`으로 과부하를 막습니다.

---

## Step 3 — 웹 콘솔 (비개발직군용, 명령줄 없이)

브라우저에서 폼으로 입력합니다. **한 줄**이면 됩니다 — React UI를 바이너리에
임베드해서 엔진을 띄웁니다:

```bash
go run ./examples/sample-api &    # (선택) 샘플 API :9000 — 당신 서비스면 생략
make web                          # UI 빌드 + 임베드 + 엔진 :8080 기동
# → http://localhost:8080
```

UI 폼: **Target base URL** = `http://127.0.0.1:9000`, **Allowlist** = `127.0.0.1`,
graph/templates JSON 붙여넣기(또는 워크로드 closed/open·페르소나 설정),
**Start node** = `browse`, **Run experiment**. 실시간 진행을 보고, 끝나면 findings를
확인하고 **View HTML report / Compare / share** 링크를 씁니다.

> ⚠️ 그냥 `make build`/`go build`는 **플레이스홀더 페이지**만 임베드합니다(진짜 UI 아님,
> 안내 문구가 뜸). 웹은 반드시 **`make web`**(= `make embed` 후 실행)로 띄우세요.
> UI를 직접 고치며 핫리로드로 개발하려면 `make dev`(vite 개발서버, `/api`를 실행 중
> 엔진으로 프록시).

---

## Step 4 — REST API 4단계 라이프사이클

모든 경로는 `/api` 아래. **생성 → 실행 → 관찰 → 공유**.

```bash
# (1) 실험 생성  →  201 {"id":"exp_..."}
EXP=$(curl -fsS -X POST "$API/experiments" -H 'Content-Type: application/json' -d '{
  "experiment": { "name":"my-run", "targetEnvId":"e", "scenarioGraphId":"shop",
                  "params": { "virtualUserCount":50, "deviationRate":0, "authStrategy":"pool" } },
  "targetEnv":  { "baseUrl":"http://127.0.0.1:9000", "allowlist":["127.0.0.1"],
                  "rateCap": { "maxRps":20000, "maxConcurrency":1000 }, "envClass":"dev" },
  "graph":      { "id":"shop", "nodes":[
                    {"id":"browse","apiTemplateId":"t_browse"},
                    {"id":"product","apiTemplateId":"t_product"},
                    {"id":"cart","apiTemplateId":"t_cart"},
                    {"id":"checkout","apiTemplateId":"t_checkout"},
                    {"id":"exit"}],
                  "edges":[
                    {"from":"browse","to":"product","weight":0.7},
                    {"from":"browse","to":"exit","weight":0.3},
                    {"from":"product","to":"cart","weight":0.45},
                    {"from":"product","to":"exit","weight":0.55},
                    {"from":"cart","to":"checkout","weight":0.6,"dependency":true},
                    {"from":"cart","to":"exit","weight":0.4}] },
  "templates":  { "t_browse":{"method":"GET","path":"/browse"},
                  "t_product":{"method":"GET","path":"/product"},
                  "t_cart":{"method":"POST","path":"/cart","payloadTemplate":"{\"productId\":\"p7\",\"qty\":1}"},
                  "t_checkout":{"method":"POST","path":"/checkout","payloadTemplate":"{\"total\":42}"} },
  "start":"browse", "maxSteps":12, "users":[{"id":"u0"},{"id":"u1"}], "seed":1
}' | jq -r .id)

# (2) 실행  →  202 {"runId":"run_..."}
RUN=$(curl -fsS -X POST "$API/experiments/$EXP/run" | jq -r .runId)

# (3) 리포트
curl -fsS "$API/runs/$RUN/report" | jq '{status:.run.status, stats:.stats, findings:.findings}'

# (4) 읽기전용 공유 링크 (PII 마스킹). ?ttl=초 로 만료 지정
curl -fsS -X POST "$API/runs/$RUN/share?ttl=86400" | jq
#  → {"token":"ab12...","url":"/reports/shared/ab12...","scope":"viewer"}
#  팀 공유 링크:  http://localhost:8080/?share=ab12...
```

### RunSpec 필드 지도

| 필드 | 설명 |
|---|---|
| `experiment` | 메타데이터 + `params.deviationRate`(0~1 이탈률), `params.authStrategy`(`"pool"`) |
| `targetEnv` | `baseUrl` / `allowlist` / `rateCap` / `envClass` |
| `graph`, `templates`, `start`, `maxSteps`, `seed` | 시나리오 |
| `users` | 가상 유저 정체성 목록 `[{id, vars?}]` (`vars`는 `{{.var}}` 템플릿값). 비밀값(토큰)은 JSON으로 받지 않음 — 인증은 `auth:`+`tmula run`(Step 5) |
| `workers?` | 분산 워커 주소 (Step 10) |
| `aggregateWorkers?` | 워커측 집계 (Step 10) |
| `workload?` | **열린(open) 모델** (Step 7) |
| `segments?` | **페르소나 믹스** (Step 8) |

---

## Step 5 — 내 API에 붙이기 (인증·per-user 값)

1. `targetEnv.baseUrl`를 내 dev/staging 서비스로, host를 `allowlist`에 추가.
2. `templates`를 실제 엔드포인트로. 헤더/페이로드에 **per-user 값 주입** 가능:

```json
{
  "t_login": { "method":"POST", "path":"/login", "payloadTemplate":"{\"user\":\"{{.subject}}\"}" },
  "t_order": { "method":"POST", "path":"/orders",
               "headers": { "Authorization": "Bearer {{.token}}" },
               "payloadTemplate": "{\"item\":\"{{.item}}\"}" }
}
```
템플릿이 채우는 값은 **두 갈래**이고, 받는 경로가 다릅니다.

**① 일반 변수(`{{.item}}` 등) — `vars`로 주입.** 비밀값이 아니므로 JSON RunSpec의 `users` 배열에 그대로 실어 POST할 수 있습니다:
```json
"users": [
  { "id":"u0", "vars": { "item":"book" } },
  { "id":"u1", "vars": { "item":"pen"  } }
]
```
> `{{.item}}`은 그 유저의 `vars`에서 채워집니다.

**② 자격증명(`{{.token}}`·`{{.subject}}`) — HTTP/JSON RunSpec으로는 주입되지 않습니다.** `domain.Credential.Secret`에 `json:"-"`가 붙어 있어(마스킹, AD-011) 요청 바디가 디코드되는 순간 토큰이 떨어져 나가기 때문입니다. `users[].cred`에 토큰을 넣어 POST해도 그 런은 **조용히 익명(미인증)으로** 돕니다 — 에러조차 나지 않습니다. 비밀값은 설계상 절대 직렬화되지 않습니다.

따라서 인증 런은 **인-프로세스** 경로로만 만듭니다: 압축 시나리오 파일의 **`auth:` 블록**(Step 1의 *시나리오 파일 포맷* 절)에 토큰을 적고 **`tmula run`**으로 실행하면, CLI가 스펙을 와이어에 태우지 않고 엔진에 직접 넘겨 비밀값을 보존합니다. 템플릿은 그대로 `{{.token}}`·`{{.subject}}`를 참조하고, 값은 풀에서 배정된 자격증명으로 채워집니다:
```yaml
# scenario.yaml (발췌)
flow:
  - id: order
    request: POST /orders
    headers: { Authorization: "Bearer {{.token}}" }
auth:
  strategy: pool
  users:
    - { subject: alice, token: jwt-aaa }   # token → {{.token}}, subject → {{.subject}}
    - { subject: bob,   token: jwt-bbb }
```
```bash
tmula run scenario.yaml --users 80
```
> closed 모델은 유저 인덱스로, open 모델은 세션(도착) 인덱스로 자격증명을 배정합니다(엔트리보다 많으면 순환). 원격 `--engine`에는 비밀값을 보낼 수 없어 미지원입니다.

3. `graph`로 실제 유저 흐름 기술 → 재실행. findings가 이제 **당신 서비스** 얘기를 합니다.

### 튜닝 노브

| 노브 | 효과 |
|---|---|
| `users` 길이 | 동시 가상 유저 수 (closed 모델) |
| `maxSteps` | 한 세션이 밟는 최대 전이 수 |
| `params.deviationRate` (0~1) | 확률적 스킵·재정렬·페이로드 변형 비율 — `mutation`/이탈 모드 |
| `rateCap` | 초당 요청·동시성 상한 (대상 보호) |
| `seed` | 같은 시드 → 같은 트래픽(재현 가능) |

---

지금까지는 **closed 모델**(고정 N명이 그래프를 반복)입니다. 아래부터가 확장 기능입니다.

## Step 6 — closed vs open 모델

- **closed**: "동시 50명 고정." 간단하지만 동시성은 내가 박은 숫자.
- **open**: "초당 λ명씩 도착." 동시 접속자 수가 **창발**합니다 (Little의 법칙 `L = λ × W`).
  이게 진짜 트래픽 모양이고, "유기적"인 부하를 만드는 길입니다.

---

## Step 7 — 열린(open) 모델 (단일 노드, 유기적)

### 7-1. 먼저 규모 산정 (capacity 엔드포인트)

> "100만 유저를 1시간에 걸쳐, 평균 세션 60초"면 동시 몇 명? 워커 몇 대 분량?

```bash
curl -fsS "$API/capacity?totalUsers=1000000&windowSeconds=3600&avgSessionSeconds=60&perWorkerCap=2000" | jq
# → { "arrivalPerSec": 277.8, "peakConcurrency": 16667, "workersNeeded": 9 }
```
초당 약 **278명 도착**, 정상상태 동시 ~16,667명. 이 숫자를 워크로드에 그대로 넣습니다.

### 7-2. 열린 모델로 실행

`workload`를 RunSpec에 추가합니다 (open이면 `users`는 정체성 1개면 충분).

```bash
curl -fsS -X POST "$API/experiments" -H 'Content-Type: application/json' -d '{
  "experiment": { "name":"open-load", "targetEnvId":"e", "scenarioGraphId":"shop",
                  "params": { "virtualUserCount":1, "deviationRate":0, "authStrategy":"pool" } },
  "targetEnv":  { "baseUrl":"http://127.0.0.1:9000", "allowlist":["127.0.0.1"],
                  "rateCap": { "maxRps":50000, "maxConcurrency":20000 }, "envClass":"dev" },
  "graph": { "id":"shop", "nodes":[{"id":"browse","apiTemplateId":"t_browse"}], "edges":[] },
  "templates": { "t_browse":{"method":"GET","path":"/browse"} },
  "start":"browse", "maxSteps":10, "users":[{"id":"u0"}], "seed":1,

  "workload": {
    "kind": "open",
    "arrival": { "shape":"constant", "startRate":278, "peakRate":278 },
    "durationSeconds": 3600,
    "maxConcurrency": 20000,
    "thinkTime": { "minMs":200, "maxMs":800 }
  }
}'
```

`workload.kind`는 `"open"` 또는 `"closed"`. 트래픽 모양은 안쪽 `arrival.shape`에서 고릅니다.

| `arrival.shape` | 의미 | 쓰는 필드 |
|---|---|---|
| `constant` | 일정 속도 | `peakRate` |
| `ramp` | startRate→peakRate 점증 | `startRate, peakRate, rampSeconds` |
| `spike` | 갑자기 튀었다 복귀 | `startRate, peakRate, rampSeconds, holdSeconds` |
| `soak` | 오래 유지 후 종료 | `peakRate, holdSeconds` |

```jsonc
// 예: 10/s에서 시작해 5분에 걸쳐 500/s까지 점증
"arrival": { "shape":"ramp", "startRate":10, "peakRate":500, "rampSeconds":300 }
```
> `thinkTime` = 스텝 사이 휴지(실감나는 페이스). `maxConcurrency` = 백프레셔 상한(넘는 도착은
> drop되며 "수요 > 용량" 신호가 됨). UI에선 Workload model을 `open`으로 바꾸면 같은 필드가 폼으로 나옵니다.
>
> ⚠️ **열린 모델은 단일 노드 전용**입니다. `workers`/`aggregateWorkers`와 같이 쓰면 400으로 거부됩니다.
> 다중 노드 스케일은 Step 10(분산 closed)을 보세요.

---

## Step 8 — 페르소나/세그먼트 (open 전용)

가중치로 **서로 다른 행동 프로파일**을 한 런에 섞습니다 — 빠른 파워유저 vs 느린 구경꾼.

```jsonc
"segments": [
  { "name": "browser", "weight": 0.7, "start": "browse" },
  { "name": "buyer",   "weight": 0.3, "start": "cart",
    "thinkTime": { "minMs": 1000, "maxMs": 3000 }, "maxSteps": 12 }
]
```
- `weight` — 도착 점유율(합이 1일 필요 없음, 상대값)
- `start` / `maxSteps` / `thinkTime` — 페르소나별 진입점·여정 길이·페이스 (생략 시 런 기본값)
- 세션 ID에 페르소나가 태깅되어 점유율 추적 가능

> 검증: 이름 유일·양수 가중치·진입 노드가 그래프에 존재해야 함. closed 모델에 넣으면 400.
> UI에선 Workload=open일 때 "Personas / segments" JSON 칸이 나옵니다.

---

## Step 9 — 실시간 모니터링 + 긴급 정지

**SSE 스트림** (열린 모델도 진행 중 라이브로 보입니다):
```bash
curl -N "$API/runs/$RUN/stream"
# data: {"status":"running","stats":{"total":1240,"errors":3,"p95":18,...}}
# data: {"status":"completed","stats":{...}}      ← 종료 시 스트림 닫힘
```

**킬 스위치** (폭주 시 즉시 중단, 부분 결과 보존):
```bash
curl -fsS -X POST "$API/runs/$RUN/kill"
```

---

## Step 10 — 분산 실행 (closed) + 워커측 집계 (다중 노드 스케일)

여러 머신에 걸쳐 대규모로 돌리는 길. 이건 **closed 모델**(워커마다 유저 샤드를 고정 실행)입니다.

### 10-1. 워커 기동 + 분산
```bash
./bin/tmula --role worker --addr :9101 &
./bin/tmula --role worker --addr :9102 &
./bin/tmula --role local  --addr :8080 &
```
스펙에 워커 주소를 추가하면 마스터가 `users`를 워커들에 쪼개 보냅니다:
```jsonc
"workers": ["127.0.0.1:9101", "127.0.0.1:9102"]
```
(또는 엔진 기동 시 `--workers ...`로 기본 풀 지정)

### 10-2. 워커측 집계 — millions-scale의 핵심
기본 분산은 요청마다 1메시지를 마스터로 스트리밍 → 수백만 요청에선 이게 병목.
`aggregateWorkers:true`면 **각 워커가 샤드 전체를 압축 요약**(카운터 + 병합 히스토그램)해
보내고 마스터가 병합합니다:
```jsonc
"workers": ["127.0.0.1:9101","127.0.0.1:9102"],
"aggregateWorkers": true
```
> 트레이드오프(문서화됨): 퍼센타일 통계는 **정확**하게 유지되지만, findings는 엔드포인트별/연속실행이
> 아닌 **run-wide** 신호로 거칠어집니다. 대신 네트워크·메모리가 요청량과 무관하게 일정.
> UI에선 Worker addresses를 채우면 "Aggregate on workers" 체크박스가 나옵니다.

---

## Step 100 — 실전 레시피 (스케일 두 축)

현재 스케일에는 두 갈래가 있고, 둘은 **서로 다른 축**입니다.

| | 트래픽 모양 | 노드 | 모델 |
|---|---|---|---|
| **A. 유기적 부하** | 도착률·ramp·spike, 페르소나 | 단일 노드 | open |
| **B. 원시 스케일** | 고정 유저 샤드 | 다중 노드 | closed + `aggregateWorkers` |

> open(도착률) + 분산은 현재 **미지원**(검증에서 거부). 한 런에서 둘을 동시에 쓸 수 없습니다.

### 레시피 A — 유기적 단일 노드 (ramp + 페르소나)
```bash
curl -fsS -X POST "$API/experiments" -H 'Content-Type: application/json' -d '{
  "experiment": { "name":"organic", "targetEnvId":"e", "scenarioGraphId":"shop",
                  "params": { "virtualUserCount":1, "deviationRate":0.05, "authStrategy":"pool" } },
  "targetEnv":  { "baseUrl":"http://127.0.0.1:9000", "allowlist":["127.0.0.1"],
                  "rateCap": { "maxRps":50000, "maxConcurrency":20000 }, "envClass":"dev" },
  "graph":      { "id":"shop", "nodes":[
                    {"id":"browse","apiTemplateId":"t_browse"},
                    {"id":"product","apiTemplateId":"t_product"},
                    {"id":"cart","apiTemplateId":"t_cart"},
                    {"id":"checkout","apiTemplateId":"t_checkout"},
                    {"id":"exit"}],
                  "edges":[
                    {"from":"browse","to":"product","weight":0.7},
                    {"from":"browse","to":"exit","weight":0.3},
                    {"from":"product","to":"cart","weight":0.45},
                    {"from":"product","to":"exit","weight":0.55},
                    {"from":"cart","to":"checkout","weight":0.6,"dependency":true},
                    {"from":"cart","to":"exit","weight":0.4}] },
  "templates":  { "t_browse":{"method":"GET","path":"/browse"},
                  "t_product":{"method":"GET","path":"/product"},
                  "t_cart":{"method":"POST","path":"/cart","payloadTemplate":"{\"productId\":\"p7\",\"qty\":1}"},
                  "t_checkout":{"method":"POST","path":"/checkout","payloadTemplate":"{\"total\":42}"} },
  "start":"browse", "maxSteps":12, "users":[{"id":"u0"}], "seed":1,

  "workload": {
    "kind": "open",
    "arrival": { "shape":"ramp", "startRate":20, "peakRate":278, "rampSeconds":600 },
    "durationSeconds": 3600,
    "maxConcurrency": 20000,
    "thinkTime": { "minMs":500, "maxMs":2000 }
  },
  "segments": [
    { "name":"browser", "weight":0.7, "start":"browse" },
    { "name":"buyer",   "weight":0.3, "start":"cart", "thinkTime":{"minMs":1000,"maxMs":3000} }
  ]
}'
```

### 레시피 B — 대규모 분산 (closed + 집계)
```bash
# 워커 N대 기동 (예시 2대)
for p in 9101 9102; do ./bin/tmula --role worker --addr :$p & done
./bin/tmula --role local --addr :8080 &

# 유저 1만 명을 jq로 생성해서 분산 + 워커측 집계
USERS=$(jq -nc '[range(10000) | {id: ("u"+(.|tostring))}]')
curl -fsS -X POST "$API/experiments" -H 'Content-Type: application/json' -d "$(jq -nc --argjson users "$USERS" '{
  experiment: { name:"scale", targetEnvId:"e", scenarioGraphId:"shop",
                params: { virtualUserCount:10000, deviationRate:0, authStrategy:"pool" } },
  targetEnv:  { baseUrl:"http://127.0.0.1:9000", allowlist:["127.0.0.1"],
                rateCap: { maxRps:50000, maxConcurrency:20000 }, envClass:"dev" },
  graph:      { id:"shop", nodes:[
                  {id:"browse",apiTemplateId:"t_browse"},
                  {id:"product",apiTemplateId:"t_product"},
                  {id:"cart",apiTemplateId:"t_cart"},
                  {id:"exit"}],
                edges:[
                  {from:"browse",to:"product",weight:0.7},
                  {from:"browse",to:"exit",weight:0.3},
                  {from:"product",to:"cart",weight:0.45},
                  {from:"product",to:"exit",weight:0.55}] },
  templates:  { t_browse:{method:"GET",path:"/browse"},
                t_product:{method:"GET",path:"/product"},
                t_cart:{method:"POST",path:"/cart",payloadTemplate:"{\"productId\":\"p7\",\"qty\":1}"} },
  start:"browse", maxSteps:12, seed:1,
  users: $users,
  workers: ["127.0.0.1:9101","127.0.0.1:9102"],
  aggregateWorkers: true
}')"
```
> 분산 closed는 `users` **배열 길이**로 워커에 분배합니다. 큰 수는 위처럼 `jq`로 생성하세요.

---

## 엔드포인트 치트시트

| 메서드 · 경로 | 용도 |
|---|---|
| `POST /api/experiments` | 실험 생성 → `{id}` |
| `GET /api/experiments/{id}` | 실험 조회 |
| `POST /api/experiments/{id}/run` | 실행 → `{runId}` |
| `GET /api/runs/{id}/report` | 통계 + findings |
| `GET /api/runs/{id}/stream` | 실시간 SSE |
| `POST /api/runs/{id}/kill` | 긴급 정지 |
| `POST /api/runs/{id}/share?ttl=초` | 읽기전용 공유 토큰 |
| `GET /api/reports/shared/{token}` | 공유 리포트 (PII 마스킹) |
| `GET /api/runs/{id}/report.html` | 단독 HTML 리포트 |
| `GET /api/runs/compare?a=&b=` | 두 런 HTML 비교 (회귀 감지) |
| `GET /api/capacity?totalUsers=&windowSeconds=&avgSessionSeconds=&perWorkerCap=` | 규모/워커 산정 |

## 자주 막히는 곳

| 증상 | 원인 / 해결 |
|---|---|
| 실험 생성 400 | `allowlist`에 baseUrl host 누락, 또는 `envClass`가 `prod` |
| open + workers 400 | 열린 모델은 단일 노드 전용 — 분산은 레시피 B(closed) 사용 |
| segments 400 | open 모델에서만 허용 + 진입 노드가 그래프에 있어야 함 |
| findings가 비어 있음 | 대상이 너무 건강하거나, 부하/시간이 부족 — 유저·duration·arrival rate를 올려보기 |
| 라이브 통계가 0 | 분산 집계(`aggregateWorkers`)는 종료 시점에 합산 — 진행 중 라이브는 단일/스트리밍 경로 |

## 권장 도입 순서

데모(Step 1) → curl로 closed 1회(Step 4) → 내 API(Step 5) → open + capacity(Step 7) →
페르소나(Step 8) → 분산 + 집계(Step 10).
