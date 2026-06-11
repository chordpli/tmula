# tmula 사용자 매뉴얼

> 이 문서는 English로도 볼 수 있습니다: [English](guide.en.md)

**tmula**는 실사용자 트래픽 시뮬레이터입니다. 한 엔드포인트에 똑같은 요청을 퍼붓는 대신, *가상 사용자*를 명시적인 **행동 그래프**(behavior graph)를 따라 움직입니다. 가상 사용자는 진짜 사람처럼 분기하고, 망설이고, 가끔 시나리오를 벗어나고, 인기 있는 엔드포인트로 몰립니다. 그리고 무엇이 깨졌는지를 **finding**으로 분류합니다. 이 가이드는 [README](../README.md)의 동반 문서입니다. 앞부분은 입문용이고 뒷부분은 레퍼런스이며, 모든 JSON 필드(처음 쓰는 사람이 가장 부담스러워하는 부분)를 다룹니다.

처음부터 끝까지 다 읽을 필요는 없습니다. 일단 돌려보고 싶다면 [실행해 보기](#실행해-보기)와 [웹 콘솔, 필드별 설명](#웹-콘솔-필드별-설명)을 읽으세요. JSON 편집기 앞에서 막히면 [JSON 형식 - 완전한 레퍼런스](#json-형식--완전한-레퍼런스)로 바로 건너뛰면 됩니다. 이 문서의 내용은 소스 코드에 근거하며, 필드 이름, 기본값, 검증 메시지는 실제 그대로 인용했습니다.

---

## 목차

1. [이 가이드는 누구를 위한 것인가 / 머릿속 모델](#이-가이드는-누구를-위한-것인가--머릿속-모델)
2. [실행해 보기](#실행해-보기)
3. [웹 콘솔, 필드별 설명](#웹-콘솔-필드별-설명)
4. [JSON 형식 - 완전한 레퍼런스](#json-형식--완전한-레퍼런스)
   - [시나리오 그래프](#시나리오-그래프)
   - [API 템플릿](#api-템플릿)
   - [페르소나 / 세그먼트](#페르소나--세그먼트)
   - [워크로드](#워크로드-json)
   - [전체 RunSpec](#전체-runspec)
   - [압축 시나리오 YAML](#압축-시나리오-yaml)
5. [CLI](#cli)
6. [Finding 자세히 보기](#finding-자세히-보기)
7. [결과 읽기](#결과-읽기)
8. [OpenAPI / HAR 가져오기](#openapi--har-가져오기)
9. [인증이 필요한 실행](#인증이-필요한-실행)
10. [분산 모드](#분산-모드)
11. [안전장치](#안전장치)
12. [예제 도메인](#예제-도메인)
13. [문제 해결 & FAQ](#문제-해결--faq)

---

## 이 가이드는 누구를 위한 것인가 / 머릿속 모델

이 가이드는 실사용자를 모으지 않고도 그들이 마주칠 버그를 찾고 싶은 모든 분을 위한 것입니다. tmula를 CI에 연결하려는 개발자일 수도 있고, 터미널은 손도 대지 않고 웹 콘솔에서만 작업하는 PM/QA일 수도 있습니다. 두 경로 모두 다룹니다. 부하 테스트 경험이 없다고 가정하고, 전문 용어는 처음 나올 때 설명합니다.

### 행동 그래프

가장 중요한 개념은 **행동 그래프**(behavior graph, *시나리오 그래프*라고도 합니다)입니다. 사용자가 API를 거쳐 가는 여정을 나타내는 방향 그래프입니다.

- **노드**(node)는 상태입니다. 대부분의 노드는 `apiTemplateId`로 **API 템플릿**(HTTP 호출 하나)에 연결됩니다. 그런 노드에 도달한다는 것은 "이 요청을 보낸다"는 뜻입니다.
- **종료 노드**(terminal node)는 `apiTemplateId`가 없습니다. 관례상 `done`과 `exit`로 이름 붙입니다. 여기에 도달하면 사용자가 또 호출하는 게 아니라 여정을 끝냈거나(`done`) 떠났다(`exit`)는 의미입니다. 요청을 보내지 않습니다.
- **엣지**(edge)는 노드 사이의 전이입니다. 각 엣지에는 **weight**(가중치), 즉 사용자가 그 엣지를 탈 상대적 확률이 있습니다. 한 노드에서 엔진은 가중치에 비례해 나가는 엣지를 고릅니다.
- **의존 엣지**(dependency edge, `dependency: true`)는 강한 전제 조건입니다. 대상 노드는 이 선행 노드를 반드시 요구하며, 사용자의 무작위 이탈이 이를 건너뛸 수 없습니다. (shop 데모에서 `cart → checkout`은 의존 엣지입니다. 장바구니 없이는 결제할 수 없습니다.)

다음은 shop 데모 그래프를 글자로 그린 그림입니다(이것이 정확히 `examples/shop` 그래프이자 웹의 "Branching shop" 프리셋입니다):

```
                 0.4
        ┌──────────────────► search ──0.65──► product ──0.45──► cart ══0.6══► checkout ──1.0──► done
        │                      │ 0.15           │ 0.25          ║ (dependency)
 browse ┤ 0.4                  ▼                ▼               ║
        ├──────────────────► category ─0.7──► product          └──0.4──► exit
        │  0.2                 │ 0.15
        └──────────► exit ◄────┘ 0.15  (every stage also drops a share into `exit`)

   ══►  dependency edge: never skipped       ──►  weighted transition
   done / exit are terminal nodes (no request) - done = completed, exit = left
```

사용자는 **시작 노드**(`browse`)에서 출발해 그래프를 엣지마다 따라가며 **최대 단계 수**(max steps)만큼 전이하고, 종료 노드에 도달하거나 단계 수가 떨어지면 멈춥니다.

### 가상 사용자

**가상 사용자**(virtual user)는 그래프를 걷는 시뮬레이션 방문자 한 명입니다. 가상 사용자가 *시간에 따라 어떻게 생성되는지*가 **워크로드 모델**(workload model)이며, 두 가지가 있습니다.

- **클로즈드 모델**(closed model). 반복하는 동시 사용자 `N`명의 고정 풀입니다. 동시 실행 수는 설정한 값 그대로입니다. "50명, 항상 50명이 진행 중"이라고 생각하면 됩니다.
- **오픈 모델**(open model). 사용자 *세션*이 시간에 따라 일정 **비율(rate)**로 도착합니다(예: 초당 새 사용자 200명). 동시 사용자 수는 도착률 × 세션 지속 시간(리틀의 법칙, Little's Law)으로 결정됩니다. 실제 공개 트래픽이 움직이는 방식과 같습니다. 공개 서비스에는 이쪽이 현실적인 기본값입니다.

단계 사이에 사용자는 **생각 시간**(think time)만큼 멈춥니다(실제 사람은 행동 사이에 읽고 판단하므로 무작위 지연을 줍니다). 오픈 모델에서는 도착하는 사용자를 **페르소나**(persona, 세그먼트)로 나눌 수 있습니다. 페르소나는 각자 자기 시작 노드와 속도를 가진 가중치 있는 사용자 유형입니다. "느린 첫 방문 둘러보기 70%, 빠른 파워 유저 30%" 같은 식으로, 단일한 로봇이 아니라 섞인 집단을 시뮬레이션할 수 있습니다.

### 세 가지 모드

tmula는 서로 겹치는 세 가지 방식으로 문제를 드러냅니다.

- **시나리오 따라가기**(scenario-following). 현실적이고 분기하는 트래픽 아래에서 정상 경로(happy path)가 버티는지 봅니다.
- **이탈**(deviation). 확률적 건너뛰기, 단계 재정렬, 페이로드 변형(의존 엣지는 위반하지 않음)으로 시나리오 밖 버그를 드러냅니다.
- **부하 집중**(load-concentration). 가상 사용자를 한 API로 몰아넣고 어디서 성능이 떨어지거나 포화되는지 봅니다.

### "finding"이란

**finding**은 tmula가 클라이언트 쪽에서 감지해 분류한 문제입니다(상태 코드, 지연 시간, 오류 패턴을 보며 서버 계측은 필요 없습니다). 종류는 `contract`, `availability`, `threshold`, `mutation` 네 가지이고 각각 심각도(severity)를 가집니다. 실행의 결과물은 "실제 트래픽이라면 무엇이 깨졌을지"입니다. 각각이 어떻게 계산되는지는 [Finding 자세히 보기](#finding-자세히-보기)를 참고하세요.

### 평범한 부하 도구와 무엇이 다른가

평범한 부하 도구는 똑같은 요청으로 URL 하나를 두들기고 처리량 숫자 하나를 알려줍니다. 그러나 결제가 장바구니가 있고 동시 실행이 높을 때만 500을 낸다는 사실은 알려줄 수 없습니다. 여정이나 의존 관계를 모델링하지 않기 때문입니다. tmula는 실제 퍼널을 걷고, 전제 조건을 지키고, 규칙 안에서 이탈하며, 실패를 종류별로 분류합니다. 그래서 숫자 하나가 아니라 버그를 얻습니다.

---

## 실행해 보기

이 절은 일부러 짧게 두었습니다. [README의 빠른 시작](../README.md#quickstart)이 정식 출처입니다. 본인에게 맞는 방법을 고르세요.

**Docker(툴체인 불필요).** 명령 하나로 콘솔(실제 UI 내장)과 예제 API 두 개를 함께 띄웁니다.

```bash
git clone https://github.com/chordpli/tmula.git && cd tmula
docker compose up
```

<http://localhost:8080>을 엽니다. Compose 네트워크 안에서 엔진은 예제 API를 **서비스 이름**으로 찾으므로 `localhost`가 아니라 `http://sample-api:9000` / `http://ticketing-api:9100`을 쓰세요. 자세한 이유는 [FAQ](#문제-해결--faq)를 참고하세요.

**설치 스크립트(미리 빌드된 바이너리, UI 내장).**

```bash
curl -fsSL https://raw.githubusercontent.com/chordpli/tmula/main/install.sh | sh
tmula --role local --addr :8080      # open http://localhost:8080
```

**소스에서 빌드**(Go 1.25+와 Node 20+ 필요):

```bash
make demo    # UI + engine + both example APIs, all locally (presets target localhost:9000/:9100)
make web     # build the React UI, embed it, serve the console on :8080
make build   # Go binary only - fast, but the UI is a placeholder page
```

> **플레이스홀더 UI vs 진짜 UI.** `make build` / `go build`는 "run `make web`"이라고 안내하는 플레이스홀더 페이지만 내장합니다. 의도된 동작입니다. CLI는 UI 빌드가 필요 없습니다. 진짜 브라우저 콘솔을 얻으려면 `make web`으로 UI를 내장하세요(또는 이미 내장되어 있는 Docker 이미지나 미리 빌드된 바이너리를 쓰세요). 콘솔이 비어 보이며 `make web`을 실행하라고 한다면 이 때문입니다.

---

## 웹 콘솔, 필드별 설명

콘솔(`make web` 이후 <http://localhost:8080>)에는 설정 카드 세 개(**Target**, **Load model**, **Scenario**)가 있고, 그 다음에 **Run** 버튼과 실시간 보기가 있습니다. 아래에 인용한 도움말 문구는 `web/src/i18n.ts`의 실제 UI 문구이며, `?` 도움말 툴팁을 그대로 옮겼습니다.

**Scenario** 카드에서 **Start from a template**를 누르고 **Branching shop**, **Concert tickets**, **Health check**, **API read flow** 중 하나를 고르면 가장 빠르게 시작할 수 있습니다. 그래프, 템플릿, 시작 노드, 최대 단계가 자동으로 채워집니다(ticketing 프리셋은 Base URL도 `http://localhost:9100`으로 바꿉니다). 그다음 손보고 Run 하면 됩니다.

### 카드: Target

> *"Where the simulated traffic goes, and the hosts it is allowed to reach. Add worker addresses to fan the load out across machines."*

| Field | 의미 | 적절한 값 | 주의점 |
|-------|------|-----------|--------|
| **Base URL** | 테스트 대상 서비스(스테이징 또는 로컬 서버). | `http://localhost:9000`, `http://sample-api:9000`(Docker) | 스킴 + 호스트(+ 포트)를 반드시 포함해야 합니다. |
| **Allowlist** | 트래픽이 닿아도 되는 호스트(쉼표 구분). 실행이 대상 밖으로 새어 나가지 못하게 막는 안전장치. | `localhost`, 또는 Docker에서는 `sample-api` | **콘솔은 Base URL 호스트를 자동으로 추가하지 않습니다.** 대상 호스트를 Base URL과 Allowlist 양쪽 모두에 넣어야 합니다. 그러지 않으면 모든 요청이 차단됩니다(아래 참고). |
| **Workers** | 선택. 부하를 분산할 gRPC 워커 주소(쉼표 구분). 비우면 이 컴퓨터에서 실행. | 빈 값, 또는 `10.0.0.5:8080,10.0.0.6:8080` | [분산 모드](#분산-모드) 참고. |
| **Aggregate on workers** | 체크박스. 각 워커가 모든 요청을 스트리밍하는 대신 자기 샤드를 요약합니다. 수백만 사용자까지 확장됩니다. | 대부분의 실행에서는 끔 | 엔드포인트별 / 실행 길이 기준 finding 정밀도를 메모리 한계와 맞바꿉니다. Workers가 설정된 경우에만 의미가 있습니다. |

> **Allowlist 함정(처음 쓰는 사람이 가장 많이 겪는 문제).** `buildRunSpec`(`web/src/api.ts`)은 Allowlist 필드를 트림하고 분리하기만 하고, Base URL 호스트를 대신 넣어 주지 않습니다. 그러면 안전 가드가 그 목록에 없는 호스트로 가는 요청을 모두 차단합니다. 그래서 Base URL을 `http://sample-api:9000`으로 두고 Allowlist를 비워 두면(또는 다른 곳을 가리키면) **모든 요청이 실패**하고 실행이 "전부 오류"로 보입니다. 항상 대상 호스트를 두 필드 모두에 넣으세요. (압축 시나리오 *파일* 경로와 CLI는 allowlist를 대상 호스트로 기본 설정하지만, 웹 콘솔은 그렇지 않습니다.)

### 카드: Load model

> *"How users hit your service. **Open** mimics organic traffic - users arrive at a rate over time. **Closed** holds a fixed pool that loops."*

| Field | 의미 | 적절한 값 | 주의점 |
|-------|------|-----------|--------|
| **Workload** | `Open`(도착률 기반, 현실적) 또는 `Closed`(반복하는 고정 풀). | 공개 서비스에는 Open | Open은 in-process 전용이라 분산 워커를 쓸 수 없습니다(RunSpec 검증 참고). |
| **Arrival rate** | Open 전용. 초당 새 사용자 수. | 처음에는 50-500 | 세션 길이와 합쳐지면 이것이 곧 동시 실행 수입니다(리틀의 법칙). |
| **Duration** | Open 전용. 사용자가 계속 도착하는 시간(초). | 30-3600 | 오픈 실행에서는 > 0이어야 합니다. |
| **Max concurrency** | Open 전용. 백프레셔 상한; `0` = 제한 없음. | 한 대에서 돌릴 땐 꼭 설정하세요! (FAQ 참고) | `0`(제한 없음)이면 무거운 오픈 실행이 자기 컴퓨터를 포화시킬 수 있습니다. 또한: ≤ 200이면 요청별 실시간 애니메이션이 켜집니다. |
| **Think time** | 사용자의 단계 사이 대기 시간(ms, 최소-최대). | `200`-`800` | `0 ≤ min ≤ max`이어야 합니다. 0이면 대기 없음(로봇처럼). |
| **Personas** | Open 전용, 고급. 각자 자기 시작 노드와 속도를 가진 가중치 있는 사용자 유형의 JSON 배열. 비우면 단일 균일 집단. | 처음에는 빈 값 | [페르소나 / 세그먼트](#페르소나--세그먼트) 참고. |

### 카드: Scenario

> *"The journey users take. Each run starts at the start node and walks the graph for up to the max steps; the JSON below defines the nodes, edges, and the API each node calls."*

| Field | 의미 | 적절한 값 | 주의점 |
|-------|------|-----------|--------|
| **Start node** | 모든 사용자가 출발하는 노드 id. | `browse`(shop) | 그래프에 존재하는 id여야 합니다. |
| **Max steps** | 사용자가 멈추기 전까지 거칠 수 있는 가장 긴 경로(전이 횟수). | shop은 10-12 | 너무 낮으면 사용자가 `checkout`까지 못 갑니다. |
| **Virtual users** | Closed: 풀 크기. Open: 대략적인 상한. | Closed 50; Open은 아무 양수나 | 오픈 실행에서는 *명목상* 값이고 세션은 도착률에서 나옵니다. 그래도 > 0이어야 합니다(실험 검증이 0을 거부). |
| **Show live traffic** | 체크박스: 실행이 스트리밍되는 동안 시각화. | 작은 실행에서는 켬 | 작을 때(≤ 200 사용자 / max-concurrency)는 요청별 애니메이션, 그 이상은 집계 흐름도. |
| **Scenario graph** (JSON) | 노드 + 가중치 엣지. *고급.* | 프리셋 사용 | 의존 엣지는 대상이 실행되기 전에 먼저 완료되어야 합니다. [시나리오 그래프](#시나리오-그래프) 참고. |
| **API templates** (JSON) | 각 노드가 보내는 요청: 메서드, 경로, 선택적 페이로드. *고급.* | 프리셋 사용 | [API 템플릿](#api-템플릿) 참고. |
| **Import** | OpenAPI 명세나 HAR 기록을 그래프 + 템플릿으로 변환. | 업로드 또는 붙여넣기 | [OpenAPI / HAR 가져오기](#openapi--har-가져오기) 참고. |

**Run**을 누르면 콘솔이 [RunSpec](#전체-runspec)을 조립해 POST 합니다. 콘솔이 자동으로 처리하는 두 가지가 있습니다. 첫째, 클로즈드 실행에서는 `users: []`와 `userCount`를 보내므로 거대한 실행도 작은 요청 본문이 됩니다(서버가 `u0..uN-1`을 합성). 둘째, 안전 `rateCap`을 설정한 부하에 맞춰 크기 조정합니다. 이 둘은 UI에서 직접 쓸 일이 없습니다.

---

## JSON 형식 - 완전한 레퍼런스

JSON 편집기가 부담스럽다면 이 절을 한 번 읽어 두세요. 각 형식은 작고 규칙적입니다. 아래의 모든 필드는 **타입**, **의미**, **예시**를 적었고, 그 뒤에 완성된 예시 하나와 검증 규칙(서버가 반환하는 정확한 오류 메시지 포함)을 붙였습니다.

> **팁:** 이 전부를 손으로 쓸 일은 거의 없습니다. 웹 **프리셋**과 **Import**가 그래프 + 템플릿을 채워 주며, CLI에서는 [압축 시나리오 YAML](#압축-시나리오-yaml)이 가장 쓰기 쉬운 작성 경로입니다. 원시 RunSpec은 파워 유저와 스크립팅용입니다.

### 시나리오 그래프

콘솔의 "Scenario graph" 편집기가 담는 그래프이자, RunSpec의 `graph` 필드입니다.

```json
{
  "id": "shop",
  "nodes": [
    { "id": "browse",   "apiTemplateId": "t_browse" },
    { "id": "checkout", "apiTemplateId": "t_checkout" },
    { "id": "done" }
  ],
  "edges": [
    { "from": "browse",   "to": "checkout", "weight": 0.6, "dependency": true },
    { "from": "checkout", "to": "done",     "weight": 1.0 }
  ]
}
```

**최상위**

| Field | Type | 의미 |
|-------|------|------|
| `id` | string | 그래프의 라벨(예: `"shop"`). |
| `nodes` | array of Node | 상태들. 최소 하나가 필요합니다. |
| `edges` | array of Edge | 전이들. 비어 있어도 됩니다(단일 노드 프로브). |

**Node**

| Field | Type | 의미 |
|-------|------|------|
| `id` | string | 고유한 노드 id. 필수이며, 비어 있거나 중복되면 안 됩니다. |
| `apiTemplateId` | string | 이 노드가 호출하는 API 템플릿. **생략하면 종료 노드**(`done` / `exit`)가 됩니다. 종료 노드는 요청을 보내지 않고 완료 또는 이탈을 표시합니다. |

**Edge**

| Field | Type | 의미 |
|-------|------|------|
| `from` | string | 출발 노드 id(`nodes`에 있어야 함). |
| `to` | string | 도착 노드 id(`nodes`에 있어야 함). |
| `weight` | number | 이 엣지를 탈 상대적 확률. `>= 0`이고 유한해야 합니다. 한 노드에서 엔진은 가중치에 비례해 나가는 엣지를 고릅니다. |
| `dependency` | bool | `true`이면 이 엣지는 강한 전제 조건입니다: 이탈이 절대 건너뛰지 않습니다. 기본값 `false`. |

**Weight 의미.** 한 노드 안에서 나가는 가중치는 상대적이며, 엔진이 정규화합니다. shop 그래프의 `browse → search (0.4)`, `browse → category (0.4)`, `browse → exit (0.2)`는 대략 40% / 40% / 20%를 뜻합니다. (압축 시나리오 경로는 더 엄격한 규칙을 적용합니다. 한 노드의 나가는 전이 가중치 합이 ≤ 1이어야 합니다. 손으로 쓴 RunSpec 그래프는 각 가중치가 `>= 0`이고 유한하기만 하면 됩니다.)

**의존 엣지**는 전제 조건을 가중치와 독립적으로 담습니다. 의존 엣지는 `weight: 0`이어도 됩니다. 워커는 요구 조건을 `dependency: true`에서 기록하고, weight-0 엣지는 일반 전이로는 건너뜁니다. 그래서 `weight: 0, dependency: true` 엣지는 "먼저 X를 했어야 한다"를 강제하면서도 지나갈 수 있는 지름길은 추가하지 않습니다.

**검증 규칙**(`domain.ScenarioGraph.Validate`):

- `scenario graph: at least one node is required`
- `scenario graph: node id must not be empty`
- `scenario graph: duplicate node id "<id>"`
- `scenario graph: edge "<from>"->"<to>" references unknown node`
- `scenario graph: edge "<from>"->"<to>" has invalid weight <v>` (음수, `NaN`, `+Inf` 가중치에서 발생합니다).

### API 템플릿

템플릿 id를 키로 하는 **맵**으로, 콘솔의 "API templates" 편집기와 RunSpec의 `templates` 필드에 들어갑니다. 각 값은 호출 가능한 엔드포인트 하나입니다.

```json
{
  "t_browse":   { "method": "GET",  "path": "/browse" },
  "t_cart":     { "method": "POST", "path": "/cart",     "payloadTemplate": "{\"productId\":\"p7\",\"qty\":1}" },
  "t_checkout": { "method": "POST", "path": "/checkout", "payloadTemplate": "{\"total\":42}",
                  "headers": { "Authorization": "Bearer {{.token}}" } }
}
```

| Field | Type | 의미 |
|-------|------|------|
| `method` | string | HTTP 메서드: `GET`, `POST`, `PUT`, … 필수. |
| `path` | string | Base URL에 이어 붙는 요청 경로. 필수. **루트 경로여야 합니다**(아래 경로 안전성 참고). |
| `payloadTemplate` | string | 선택적 요청 본문(문자열). 변수 보간을 지원합니다(아래). |
| `headers` | object (string→string) | 선택적 정적 요청 헤더. 값에 보간을 지원합니다. |

> 전체 도메인 `APITemplate`에는 `id`와 `protocol` 필드도 있지만, 템플릿 *맵*에서는 키가 id이고 protocol은 `rest`로 기본 설정됩니다. 위의 압축 맵 형태(`{ method, path, payloadTemplate?, headers? }`)가 프리셋, 임포터, UI가 모두 쓰는 형태입니다.

**변수 보간.** 본문과 헤더 값은 요청마다 Go의 `text/template` 문법 `{{.name}}`으로 렌더링되는 템플릿입니다.

- `{{.var}}`. 가상 사용자 자신의 `vars` 맵에서 온 값(RunSpec에서 사용자별 객체를 직접 제공할 때).
- `{{.token}}`. 이 사용자/세션에 배정된 자격 증명 비밀(secret). [인증이 필요한 실행](#인증이-필요한-실행)에서만 존재합니다.
- `{{.subject}}`. 자격 증명의 비민감 주체(예: 사용자명).

흔한 인증 헤더는 `"Authorization": "Bearer {{.token}}"`입니다.

**경로 안전성 규칙**(`runspec.validateTemplatePath`). `path`는 반드시 다음을 지켜야 합니다.

- 단일 `/`로 시작해야 합니다(루트 경로). 아니면 `must be a rooted path starting with /`.
- `//`로 시작하면 안 됩니다. 아니면 `must not start with // (protocol-relative authority)`.
- `://`를 포함하면 안 됩니다. 아니면 `must not contain a scheme`.
- `\r`, `\n`, `\t`를 포함하면 안 됩니다. 아니면 `must not contain control characters`.

이 규칙들은 템플릿 경로가 요청을 대상 호스트 밖으로 돌리지 못하게 합니다. (경로로 *렌더링되는* 변수는 요청 시점에 allowlist 가드가 추가로 잡습니다.) 또한 `method`가 비어 있으면 `api: template "<id>": method is required`.

### 페르소나 / 세그먼트

**오픈 모델 전용.** 콘솔의 "Personas" 편집기와 RunSpec의 `segments` 필드입니다. 도착하는 사용자에서 추출되는, 가중치 있는 행동 프로필의 JSON 배열입니다. 각 재정의는 선택입니다. `name`과 `weight`만 설정한 세그먼트는 실행 기본값처럼 동작하지만 자기 정체성으로 집계됩니다.

```json
[
  { "name": "browser", "weight": 0.7, "start": "browse" },
  { "name": "buyer",   "weight": 0.3, "start": "cart",
    "maxSteps": 4, "thinkTime": { "minMs": 100, "maxMs": 300 } }
]
```

| Field | Type | 의미 |
|-------|------|------|
| `name` | string | 페르소나에 라벨을 붙이고 세션에 태그를 답니다. 실행 내에서 **필수이며 고유**해야 합니다. |
| `weight` | number | 도착에서 이 세그먼트가 차지하는 상대적 몫. `> 0`이어야 합니다. 가중치 합이 1일 필요는 없습니다. 각 세그먼트의 확률은 자기 가중치를 전체로 나눈 값입니다. |
| `start` | string | 선택. 이 페르소나의 시작 노드를 실행 기본값 대신 재정의합니다. 빈 값 = 실행 기본값. 그래프에 있는 노드여야 합니다. |
| `maxSteps` | int | 선택. 단계 상한을 재정의합니다. `0` = 실행 기본값. `>= 0`이어야 합니다. |
| `thinkTime` | object | 선택. 실행의 생각 시간을 재정의하는 `{ "minMs": int, "maxMs": int }`. `0 <= minMs <= maxMs`를 만족해야 합니다. |

**검증 규칙**(`domain.ValidateSegments` + RunSpec 검사):

- `segment <i>: name is required`
- `segment "<name>": duplicate name`
- `segment "<name>": weight must be > 0 (got <v>)`
- `segment "<name>": maxSteps must be >= 0 (got <n>)`
- `api: segments (personas) apply only to the open workload model` (클로즈드 실행의 세그먼트는 거부됩니다).
- `api: segment "<name>" start node "<id>" is not in the graph`

### 워크로드 (JSON)

RunSpec의 `workload` 필드는 사용자가 어떻게 생성되는지를 고릅니다. 생략하면(또는 클로즈드 모델을 쓰면) 기본 고정 풀 동작이 됩니다.

**오픈 모델**

```json
{
  "kind": "open",
  "arrival": {
    "shape": "ramp",
    "startRate": 50,
    "peakRate": 500,
    "rampSeconds": 60,
    "holdSeconds": 600
  },
  "durationSeconds": 700,
  "maxConcurrency": 2000,
  "thinkTime": { "minMs": 200, "maxMs": 800 }
}
```

| Field | Type | 의미 |
|-------|------|------|
| `kind` | string | `"open"` 또는 `"closed"`. |
| `arrival.shape` | string | `constant` \| `ramp` \| `spike` \| `soak`. 도착률이 시간에 따라 어떻게 움직이는지. |
| `arrival.startRate` | number | t=0에서 초당 도착 수(ramp/spike의 기준). |
| `arrival.peakRate` | number | 최고점에서 초당 도착 수. |
| `arrival.rampSeconds` | int | 최고점까지 올라가는 데 드는 초. |
| `arrival.holdSeconds` | int | 최고점에서 유지하는 초. |
| `durationSeconds` | int | 계속 도착하는 시간. `> 0`이어야 합니다. |
| `maxConcurrency` | int | 진행 중 요청에 대한 백프레셔 상한. `0` = 제한 없음. `>= 0`이어야 합니다. |
| `thinkTime` | object | `{ "minMs": int, "maxMs": int }`, 사용자의 단계 사이 대기. |

**Shape.** `constant`는 한 비율을 유지하고, `ramp`는 `startRate`에서 `peakRate`로 올라가며, `spike`는 최고점으로 튀고, `soak`는 길게 꾸준히 돕니다. `ramp`와 `spike`에서는 `peakRate`가 **> 0이어야 합니다**(이들은 최고점으로 정의됩니다. `constant`/`soak`는 `startRate`로 되돌아가지만 ramp/spike는 그렇지 않습니다).

**클로즈드 모델**

```json
{ "kind": "closed", "concurrency": 50, "thinkTime": { "minMs": 0, "maxMs": 0 } }
```

| Field | Type | 의미 |
|-------|------|------|
| `kind` | string | `"closed"`. |
| `concurrency` | int | 반복하는 사용자의 고정 수. `> 0`이어야 합니다. |
| `thinkTime` | object | 위와 동일. |

**검증 규칙**(`domain.WorkloadModel.Validate`):

- `workload: invalid kind "<k>"`
- `think time: require 0 <= minMs <= maxMs (got <a>..<b>)`
- closed: `workload: closed model needs concurrency > 0`
- open: `workload: invalid arrival shape "<s>"`; `workload: arrival rates must be finite`; `workload: arrival rates must be non-negative`; `workload: open model needs a positive arrival rate`; `workload: ramp arrival needs peakRate > 0`(그리고 `spike`); `workload: open model needs durationSeconds > 0`; `workload: maxConcurrency must be >= 0`.

### 전체 RunSpec

자기 완결적인 실행 정의이자 `POST /api/experiments`의 원시 본문입니다. 스크립팅이나 고급 제어에만 필요합니다. 콘솔과 시나리오 파일이 알아서 만들어 줍니다. 다음이 실제 형태입니다(`runspec.RunSpec`과 `web/src/api.ts`가 POST 하는 내용 기준):

```json
{
  "experiment": {
    "name": "ui-run",
    "targetEnvId": "env",
    "scenarioGraphId": "graph",
    "params": { "virtualUserCount": 50, "deviationRate": 0, "authStrategy": "pool" }
  },
  "targetEnv": {
    "id": "env",
    "baseUrl": "http://localhost:9000",
    "allowlist": ["localhost"],
    "rateCap": { "maxRps": 1000, "maxConcurrency": 200 },
    "envClass": "dev"
  },
  "graph": { "...": "see Scenario graph" },
  "templates": { "...": "see API templates" },
  "start": "browse",
  "maxSteps": 12,
  "users": [],
  "userCount": 50,
  "seed": 1,
  "workload": { "...": "optional; see Workload" },
  "segments": [],
  "trace": true,
  "workers": [],
  "aggregateWorkers": false,
  "credentialPool": null
}
```

**최상위 필드**

| Field | Type | 의미 |
|-------|------|------|
| `experiment` | object | 실행과 실행 시 파라미터에 이름을 답니다. `name` 필수; `params.virtualUserCount`는 `> 0`; `params.deviationRate`는 `[0,1]`; `params.authStrategy`는 `pool` 또는 `bootstrap-signup`. |
| `targetEnv` | object | 테스트 대상 시스템 + 안전 제약. `baseUrl` 필수; `allowlist` 비어 있지 않음; `rateCap.maxRps`와 `rateCap.maxConcurrency` 둘 다 `> 0`; `envClass`는 `dev`, `staging`, `prod-locked` 중 하나. |
| `graph` | object | [시나리오 그래프](#시나리오-그래프). |
| `templates` | object | [API 템플릿](#api-템플릿) 맵. |
| `start` | string | 시작 노드 id. 필수. |
| `maxSteps` | int | 사용자당 최대 전이 횟수. |
| `users` | array | 명시적 가상 사용자 풀(클로즈드). 보통 비워 두고 서버가 합성하게 둡니다. |
| `userCount` | int | `users`가 비어 있을 때 클로즈드 풀 크기를 정합니다: 서버가 실행 시점에 `u0..u{userCount-1}`을 합성하므로 거대한 실행도 작은 요청 본문이 됩니다. 명시적 `users` 목록이 우선합니다; 오픈 모델은 이를 무시합니다. |
| `seed` | int64 | 재현성을 위한 난수 시드(시나리오 파일은 이를 `1`로 기본 설정). |
| `workload` | object | 선택적 [워크로드 모델](#워크로드-json). Nil/클로즈드 = 고정 풀(기본). |
| `segments` | array | [페르소나 믹스](#페르소나--세그먼트)(오픈 전용). |
| `trace` | bool | 작은 실행을 트래픽 그래프용 요청별 실시간 스트리밍에 참여시킵니다. 큰 실행은 이를 무시합니다. |
| `workers` | array of string | 분산할 gRPC 워커 주소. 비어 있으면 로컬 실행. |
| `aggregateWorkers` | bool | 워커가 모든 요청을 스트리밍하는 대신 자기 샤드를 압축 요약으로 접습니다. `workers`가 설정되지 않으면 무시됩니다. |
| `credentialPool` | object | 선택적 [인증 풀](#인증이-필요한-실행). Nil = 비인증. |

> **`virtualUserCount`는 오픈 실행에서도 > 0이어야 합니다.** 여기서 자주 헷갈립니다. `experiment.params.virtualUserCount`는 워크로드 모델과 무관하게 `> 0`으로 검증됩니다(`experiment: virtualUserCount must be > 0`). 오픈 실행에서는 *명목상* 필드이고 실제 사용자는 도착률에서 나오지만, 그래도 양수를 설정해야 합니다. 시나리오 파일 경로는 오픈 실행에서 이를 자동으로 `1`로 설정합니다.

**RunSpec 수준의 핵심 검증 규칙**(`runspec.RunSpec.Validate`):

- `api: start node is required`
- `api: at least one virtual user is required` (오픈이 아닌 모든 경로는 `len(users) > 0` *또는* `userCount > 0`이 필요합니다).
- `api: distributed workers are not supported with the open workload model` (오픈 모델은 in-process 전용입니다).
- `api: a credential pool is not yet supported with distributed workers`.

### 압축 시나리오 YAML

가장 쓰기 쉬운 작성 경로입니다. `tmula run scenario.yaml`은 이 압축 문서를 읽고 `Expand`하여 전체 RunSpec으로 만들며, 나머지는 기본값으로 채웁니다. YAML *또는* JSON을 쓸 수 있습니다(둘 다 파싱되며 필드 이름은 json 태그와 일치). 모든 블록을 사용하는 완성 예시는 다음과 같습니다.

```yaml
target: http://127.0.0.1:9000     # the system under test (required)
allow: [127.0.0.1]                # hosts the run may reach (defaults to the target's host)
users: 80                         # closed-model pool size (default 20; ignored when `open:` is set)
maxSteps: 10                      # default: the flow length
seed: 1                           # default 1

flow:                             # the ordered journey (required, >= 1 step)
  - id: browse
    request: GET /browse          # "METHOD /path" shorthand
  - id: search
    request: GET /search
    weight: 0.7                   # probability of the edge to the next step (default 1)
  - id: cart
    request: POST /cart
    body: '{"productId":"p7","qty":1}'
    headers: { X-Client: tmula }
  - id: checkout
    request: POST /checkout
    body: '{"total":42}'
    dependsOn: cart               # marks the edge into checkout as a never-skipped dependency

# Switch to an organic (open) arrival-rate load instead of a fixed pool:
open:
  rate: 200                       # constant arrivals/sec  (or from/to + rampSeconds for a ramp)
  forSeconds: 30                  # required for an open run
  thinkMs: [200, 800]             # [min, max] pause between a user's steps
  maxConcurrency: 2000            # back-pressure cap (0 = uncapped)

# Optional persona mix (open model only):
segments:
  - { name: browser, weight: 0.7, start: browse }
  - { name: buyer,   weight: 0.3, start: cart }

# Optional auth - see "Authenticated runs":
auth:
  strategy: pool                  # only "pool" is supported here today
  users:
    - { subject: alice, token: jwt-aaa }
    - { subject: bob,   token: jwt-bbb }
```

**Scenario 필드**

| Field | Type | 의미 / 기본값 |
|-------|------|---------------|
| `target` | string | Base URL. 필수. |
| `allow` | array | 허용 호스트. **기본값은 대상의 호스트**(파일 경로는 웹 콘솔과 달리 이를 *직접* 도출해 줍니다). |
| `flow` | array of Step | 여정. 필수, ≥ 1 단계. |
| `users` | int | 클로즈드 풀 크기. 기본값 `20`. `open`이 설정되면 무시됩니다. |
| `maxSteps` | int | 걷기 상한. 기본값: flow 길이. |
| `seed` | int64 | 재현성. 기본값 `1`. |
| `open` | object | 오픈 모델로 전환(아래). |
| `segments` | array | 페르소나 믹스; `open`이 필요합니다(아니면 `scenariofile: segments require an open workload`). |
| `auth` | object | 자격 증명(아래). |

**Step 필드**

| Field | Type | 의미 |
|-------|------|------|
| `id` | string | 고유한 노드/템플릿 id. 필수. |
| `request` | string | `"METHOD /path"` 약식. 비우면 순수 상태 노드(요청 없음). |
| `body` | string | 요청 페이로드 템플릿. |
| `headers` | object | 정적 요청 헤더. |
| `dependsOn` | string | 이 단계가 요구하는 앞선 단계의 id; 이 단계로 들어오는 엣지가 절대 건너뛰지 않는 의존 엣지가 됩니다. |
| `weight` | number | *다음* 단계로 가는 엣지의 확률. 기본값 `1`. |

**`open` 필드**([워크로드](#워크로드-json) 모델로 매핑됨)

| Field | Type | 의미 |
|-------|------|------|
| `rate` | number | 상수 초당 도착 수. |
| `from` / `to` | number | 램프 시작 / 최고 비율. |
| `rampSeconds` / `holdSeconds` | int | 램프 / 유지 시간. |
| `shape` | string | 재정의: `constant`\|`ramp`\|`spike`\|`soak`. `from`/`to`가 주어지면 `ramp`, 아니면 `constant`로 기본 설정. |
| `forSeconds` | int | **필수.** 계속 도착하는 시간(`scenariofile: open.forSeconds must be > 0`). |
| `thinkMs` | `[min, max]` | 생각 시간 범위(정확히 정수 두 개여야 함, 아니면 `open.thinkMs must be [min, max]`). |
| `maxConcurrency` | int | 백프레셔 상한. |

**`auth` 필드.** [인증이 필요한 실행](#인증이-필요한-실행) 참고. `strategy`는 `pool`로 기본 설정됩니다(여기서는 `pool`만 허용). `users`는 `{ subject, token }` 목록입니다(`token`이 비밀이며, 템플릿에는 `{{.token}}`으로 노출).

`Expand`가 기본값을 채우는 방식은 다음과 같습니다. `flow`에서 그래프와 템플릿을 도출하고(요청을 가진 각 단계는 `t_<id>` 템플릿이 됨), 연속 단계를 가중치 엣지로 연결하며, `rateCap`은 `{ maxRps: 10000, maxConcurrency: 1000 }`, `envClass`는 `dev`, `start`는 첫 단계로 설정합니다. 압축 그래프는 더 엄격한 시나리오 규칙으로 검증됩니다(전이 가중치 `[0,1]`, 노드별 나가는 합 ≤ 1, 의존 엣지가 DAG를 형성).

### Graph-first 시나리오 파일

여정이 분기하면 선형 `flow`로는 부족합니다. 시나리오 파일은 `flow` 대신 **그래프 자체**를 담을 수 있습니다(`flow`와 상호 배타). [액세스 로그 학습](#액세스-로그에서-그래프-학습하기)이 내놓는 형식이 바로 이것이고, 손으로 써도 됩니다.

```yaml
target: http://localhost:9000
start: browse                      # 필수: 모든 세션의 시작 노드
maxSteps: 12                       # 기본값: 노드 수
graph:                             # 시나리오 그래프 (JSON 레퍼런스와 동일한 형태)
  id: shop
  nodes:
    - { id: browse,   apiTemplateId: t_browse }
    - { id: checkout, apiTemplateId: t_checkout }
    - { id: exit }
  edges:
    - { from: browse, to: checkout, weight: 0.6, dependency: true }
    - { from: browse, to: exit,     weight: 0.4 }
templates:                         # API 템플릿 맵 (키 = id, protocol은 rest로 기본)
  t_browse:   { method: GET,  path: /browse }
  t_checkout: { method: POST, path: /checkout, payloadTemplate: '{"total":42}' }
```

`graph`를 쓰면 `start`가 필수이고(그래프에 있는 노드여야 함), 템플릿을 가리키는 모든 노드의 템플릿이 `templates`에 있어야 하며, `open` / `segments` / `auth` / `users` 등 나머지 블록은 flow 형식과 똑같이 동작합니다. 검증도 같은 엄격한 시나리오 규칙을 통과해야 합니다.

---

## CLI

`tmula` 바이너리는 서브커맨드를 가진 명령 하나입니다. 인식되는 서브커맨드가 없으면 장기 실행 엔진(`serve`)을 시작합니다.

### `tmula run` - 시나리오를 실행하고 finding 출력

시나리오 파일(또는 단일 엔드포인트 플래그)에서 RunSpec을 만들고 실행한 뒤 finding을 출력합니다. 기본은 in-process이며, `--engine`으로 실행 중인 엔진을 대상으로 삼을 수도 있습니다.

| Flag | 기본값 | 의미 |
|------|--------|------|
| `--target <url>` | (파일에서) | 대상 base URL; 시나리오 파일의 target을 덮어씁니다. |
| `--get <path>` | - | 단일 엔드포인트 모드: 이 경로를 GET(시나리오 파일 없음). |
| `--post <path>` | - | 단일 엔드포인트 모드: 이 경로를 POST. |
| `--users <n>` | 0 | 클로즈드 모델 가상 사용자 수. |
| `--open <rate>` | 0 | 오픈 모델: 초당 도착 수. |
| `--for <s>` | 0 | 오픈 모델: 계속 도착하는 시간(초). |
| `--ramp-to <rate>` | 0 | 오픈 모델: 램프 최고 비율(`--open`을 시작값으로 사용). |
| `--seed <n>` | 1 | 난수 시드. |
| `--engine <url>` | - | in-process 대신 기존 엔진에 HTTP로 실행. |
| `--json` | false | 요약 대신 원시 보고서 JSON 출력. |
| `--fail-on-findings` | false | finding이 하나라도 감지되면 비정상 종료(CI 게이트). |
| `--fail-on-severity <s>` | - | `warning` 또는 `critical` 이상 finding에 대해서만 게이트. |
| `--summary <file>` | `$GITHUB_STEP_SUMMARY` | 마크다운 실행 요약(지표 + finding 표)을 이 파일에 덧붙입니다. 해당 환경변수가 설정된 GitHub Actions에서는 기본으로 스텝 요약에 실리므로 CI에서 설정 없이 동작합니다. |
| `--timeout <dur>` | 2m | 실행 완료를 기다리는 최대 시간. |

**실전 예시:**

```bash
# A scenario file with a fixed pool of 50 users
tmula run examples/shop/scenario.yaml --users 50

# Single endpoint, no file - a quick "is it healthy under 20 users?" probe
tmula run --target http://localhost:9000 --get /health --users 20

# Organic open load: 278 arrivals/sec for one hour
tmula run examples/shop/scenario.yaml --open 278 --for 3600

# A ramp: start at 50/sec, climb to 500/sec, over the run
tmula run examples/shop/scenario.yaml --open 50 --ramp-to 500 --for 600

# CI gate: exit 2 if anything broke
tmula run examples/shop/scenario.yaml --users 50 --fail-on-findings

# CI gate, criticals only
tmula run examples/shop/scenario.yaml --users 50 --fail-on-severity critical
```

**종료 코드:** `0` 정상 · `1` 오류(또는 failed/killed 실행) · `2` 게이트 아래 finding 감지됨. 게이트는 `--fail-on-findings` 또는 `--fail-on-severity warning`에 대해서는 모든 finding을 세고, `--fail-on-severity critical`은 critical만 셉니다. 깨끗하게 완료되지 못한 실행(failed 또는 killed, 예를 들어 타임아웃이나 kill-switch 작동)은 finding과 **무관하게** 비정상 종료하므로, CI 게이트를 조용히 통과하지 않습니다.

플래그 파서는 위치 인자를 루프로 수집하므로 `tmula run scenario.yaml --users 50`과 `tmula run --users 50 scenario.yaml`이 모두 동작합니다.

### CI에서 쓰기

위의 종료 코드 덕분에 `tmula run`은 머지 게이트가 됩니다. 잡이 방금 빌드한 서비스에 여정을 돌리고, 여정이 깨지면 스텝이 실패합니다. 저장소에는 바이너리 설치 → 시나리오 실행 → 마크다운 요약을 워크플로 페이지에 게시 → (선택) PR 코멘트까지 해 주는 **GitHub Action**이 들어 있습니다:

```yaml
jobs:
  journey:
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write        # comment: true일 때만 필요
    steps:
      - uses: actions/checkout@v4
      - run: docker compose up -d my-service   # SUT를 평소 방식으로 기동
      - uses: chordpli/tmula@main
        with:
          scenario: tests/journey.yaml
          target: http://localhost:9000
          users: 50
          fail-on: critical        # findings | warning | critical | none
          comment: true            # 요약을 PR 코멘트로
```

액션 없이도 맨 `tmula run`이 GitHub Actions와 협력합니다. `GITHUB_STEP_SUMMARY`가 설정돼 있으면 마크다운 요약이 자동으로 덧붙고, 실행이 실패했거나 게이트가 작동했을 때도 요약은 기록됩니다 - 빨간 잡이 종료 코드가 아니라 *무엇이 깨졌는지*로 바로 이어집니다.

### `tmula bench` - 용량 프로브

목표 동시 실행 수로 bench 하니스를 SUT에 대해 구동하고 용량 지표(달성 RPS, 지연 백분위수, 추적 오차)를 출력합니다. 컨트롤 플레인을 거치지 않고 bench 하니스를 직접 씁니다.

| Flag | 기본값 | 의미 |
|------|--------|------|
| `--target` / `--get` / `--post` | - | `run`과 동일한 시나리오 / 단일 엔드포인트 형태. |
| `--users <n>` | 50 | 목표 동시 실행 수. |
| `--max-steps <n>` | flow 길이 | 사용자당 최대 전이 횟수. |
| `--timeout <dur>` | 10s | 요청당 전송 타임아웃. |
| `--seed <n>` | 1 | 시드. |
| `--json` | false | 원시 결과 JSON. |

```bash
tmula bench examples/shop/scenario.yaml --users 100
tmula bench --target http://localhost:9000 --get /health --users 50
```

### `tmula init` - 명세나 트래픽에서 시나리오 스캐폴딩

기존 API 설명(OpenAPI 또는 HAR)이나 **액세스 로그**를 시나리오 파일로 바꿉니다. 빈 페이지가 아니라 실제 엔드포인트 - 또는 실제 트래픽 - 에서 시작할 수 있습니다.

| Flag | 기본값 | 의미 |
|------|--------|------|
| `--from <file>` | - | **필수.** OpenAPI, HAR, 또는 액세스 로그 파일. |
| `--format <f>` | auto | `auto` \| `openapi` \| `har` \| `accesslog`. |
| `--out <file>` | stdout | 시나리오를 쓸 위치. |
| `--target <url>` | - | 대상 base URL 재정의(로그는 호스트를 담지 않으므로 필수). |

```bash
tmula init --from examples/imports/shop.openapi.yaml --out scenario.yaml
tmula init --from access.log --target http://staging:9000 --out scenario.yaml
tmula run scenario.yaml --users 50
```

OpenAPI/HAR는 선형 flow를 스캐폴딩하지만, 액세스 로그는 [트래픽에서 그래프를 *학습*](#액세스-로그에서-그래프-학습하기)해 분기 가중치까지 채운 graph-first 시나리오를 냅니다.

### `serve`(기본)와 역할

`run` / `bench` / `init` 없이 호출하면 장기 실행 엔진이 시작됩니다.

| Flag | 기본값 | 의미 |
|------|--------|------|
| `--role <r>` | local | `local` \| `master` \| `worker`. |
| `--addr <addr>` | :8080 | HTTP 수신 주소(컨트롤 플레인 + UI); 워커의 경우 gRPC 수신 주소. |
| `--workers <csv>` | - | 쉼표 구분 워커 주소; 자체 워커가 없는 실험은 이들에 분산됩니다. |
| `--store <file>` | - | local 역할: JSON 스냅샷 파일, 시작 시 로드되고 정상 종료 시 기록되어 재시작 후에도 실행 이력이 남습니다. |
| `--db-dsn <dsn>` | - | master 역할: 영속 저장소용 Postgres DSN(비어 있으면 in-memory로 폴백; 미설정 시 환경변수 `TMULA_DB_DSN` 사용). |
| `--version` | - | 버전 출력 후 종료. |

```bash
tmula --role local  --addr :8080                                  # engine + API + embedded UI
tmula --role worker --addr :9101                                  # a distributed worker (gRPC)
tmula --role local  --addr :8080 --workers 127.0.0.1:9101,127.0.0.1:9102   # a master with a worker pool
```

헬스 체크: `GET /healthz`는 `{"status":"ok","role":...,"version":...}`를 반환합니다.

---

## Finding 자세히 보기

finding은 `{ runId, category, severity, evidenceRef, firstSeen, description }`입니다. 종류는 네 가지, 심각도는 세 가지(`critical`, `warning`, `info`)입니다. 단일 노드 경로는 API별·종류별로 분류하므로, 나쁜 엔드포인트 하나가 수백 개가 아니라 finding *하나*를 냅니다. 순서는 mutation → contract → availability → threshold입니다. 각각의 계산 방식은 다음과 같습니다(`server/internal/obs/finding.go`). 먼저, 곳곳에서 쓰이는 두 술어입니다.

- 요청이 **failed**인 경우: `statusCode >= 400` **또는** `errorClass`를 가짐(예: `"timeout"`, `"transport"`, `"assertion"`).
- 요청이 **unavailable**인 경우: `statusCode >= 500` **또는** `errorClass == "timeout"` **또는** `errorClass == "transport"`.

| Category | Severity | 정확히 어떻게 계산되는가 |
|----------|----------|--------------------------|
| **contract** | critical | **변형되지 않은** 요청이 **5xx**를 반환했거나 **assertion**에 실패함(`contractSignal`). "정상 경로가 개발자가 놓쳤을 법한 오류를 낸 것"입니다. API당 finding 하나: *"N contract violation(s) on `<api>` (unexpected error on the happy path)."* |
| **mutation** | warning | **변형된** 입력이 **failed**됨(`mutationSignal`). 변형 테스트는 입력을 일부러 실패시키므로 이는 정보 제공용입니다. API당 finding 하나: *"mutated input surfaced N error(s) on `<api>`."* |
| **availability** | critical | **연속 `unavailable()` 결과가 `AvailabilityRun` 이상** 발생한 API. 연속 구간은 엔드포인트별로 타임스탬프 순서로 평가됩니다(그래서 결과가 스트리밍되어 들어오는 순서에 견고합니다). `AvailabilityRun <= 0`이면 비활성화됩니다. API당 finding 하나: *"N consecutive failures on `<api>` (saturation or downtime)."* |
| **threshold** | warning | 실행 전체 기준, 변형 요청 제외. 가능한 finding 두 개: 오류율(`failed / total`)이 `ErrorRateThreshold`**보다 큼** → *"error rate X exceeded threshold Y"*; 그리고 p95 지연이 `P95LatencyMs`**보다 큼**(그 게이트가 `> 0`일 때) → *"p95 latency Xms exceeded threshold Yms."* |

### `ClassifyConfig` 조정

임계값은 `ClassifyConfig`에 있습니다.

| Field | 의미 |
|-------|------|
| `ErrorRateThreshold` | 전체 오류율이 이보다 크면 → threshold finding. `0`이면 오류율 게이트 비활성화. |
| `P95LatencyMs` | 전체 p95 지연이 이보다 크면 → threshold finding. `0`이면 p95 게이트 비활성화. |
| `AvailabilityRun` | 한 API에서 이만큼 연속 실패하면 → availability finding. `0`이면 availability 감지 비활성화. |

> **분산/집계 경로에 대한 참고.** 워커가 샤드를 집계하면(`aggregateWorkers`), finding은 병합된 `Summary`(`FindingsFromSummary`)에서 나오며, 이는 종류별 집계만 보관합니다(API별 분해도, 순서도 없음). 그래서 실행 전체에 대해 작동한 종류당 최대 *하나*의 finding을 내는데, 엔드포인트별 단일 노드 finding보다 일부러 더 거칩니다. 정밀도와 규모를 맞바꾼 결과입니다. (미묘한 점: Summary의 threshold 오류율/p95는 변형된 것을 포함해 *모든* 관측을 세는 반면, 단일 노드 분류기는 변형 요청을 제외합니다. 분산 경로가 변형 관측을 운반하지 않기 때문에 현재로서는 잠재적인 차이입니다.)

---

## 결과 읽기

**Show live traffic**가 켜져 있으면 콘솔이 실시간 보기를 스트리밍하고, 끝나면 링크가 있는 보고서를 받습니다.

**트래픽 흐름도.** 요청은 왼쪽에서 들어와 오른쪽 결과를 향해 펼쳐집니다(퍼널). 엣지 **두께는 요청량**이고(로그 스케일이라 12-요청 엣지와 12-백만-요청 엣지가 한 화면에서 함께 읽힙니다), 엣지 **색은 오류 비율에 따라 초록에서 빨강으로** 물듭니다. 종료 노드로 들어가는 엣지는 결과로 렌더링됩니다. `done`으로의 유입은 **완료**(completed), `exit`로의 유입은 **이탈**(left)로 읽힙니다. 이는 요청이 아니라 여정의 결과이므로 "N requests" 헤드라인에서 제외됩니다. 작은 실행(≤ 200 사용자, 또는 오픈 모델에서 ≤ 200 max-concurrency)에서는 **각 요청을 점으로 애니메이션**합니다(초록 = 정상, 빨강 = 오류). 그 상한을 넘으면 집계 흐름도(엣지별 카운트)로 폴백하는데, 페이로드가 요청 수가 아니라 엣지 수로 묶이므로 어떤 실행 규모에도 확장됩니다.

**지연 히트맵.** 2차원 히스토그램입니다. 행은 지연 구간(낮음 → 높음, 맨 위 구간은 상한 없음, 예: "5s+"), 열은 실행 시작 이후 시간 버킷, 각 셀의 색은 그 구간 × 시간의 요청 수입니다(진할수록 밀도 높음). "지연이 어디로 갔나"를 보는 뷰입니다. 맨 위에 얇고 뜨거운 꼬리가 있으면 느린 소수가 있다는 뜻입니다.

**실시간 지표.** 진행 카운터: 요청 수, 오류율, p50 / p95 / p99 / 최대 지연, 타임아웃, 그리고 상태 코드 집계(예: `200:313 500:8`).

**HTML 보고서 & 비교.** 모든 실행에는 독립 실행형 서버 렌더링 **HTML 보고서**(`View full HTML report`)와 회귀를 찾기 위한 실행 간 **비교** 뷰(`Compare with previous run`)가 있습니다.

**공유 링크.** 보고서에 대해 읽기 전용 **viewer** 공유 링크를 발급할 수 있습니다. viewer에게는 *"Read-only. Sensitive fields are redacted."*라고 안내됩니다. 특히 실행의 `killReason`은 공유 보고서에서 가려지므로, 내부 kill reason이 외부 viewer에게 새지 않습니다. 공유 링크에는 만료를 둘 수 있으며, 만료되었거나 알 수 없는 토큰은 현지화된 "expired"/"not found" 메시지를 냅니다.

---

## OpenAPI / HAR 가져오기

그래프 + 템플릿을 손으로 작성할 일은 거의 없습니다. 임포터가 **OpenAPI** 명세, **HAR** 기록, 또는 **액세스 로그**를 편집 가능한 시나리오(그래프 + 템플릿 + start + maxSteps)로 바꿔 줍니다.

- **웹 콘솔:** Scenario 카드 → **Import from OpenAPI / HAR** → 파일 업로드 또는 텍스트 붙여넣기 → **Import**. 그래프와 템플릿 필드가 채워지면 검토하고 Run 하세요. (엔드포인트는 `POST /api/import?format=auto|openapi|har|accesslog`입니다.)
- **CLI:** `tmula init --from <openapi.yaml|session.har|access.log> --out scenario.yaml`.

**형식 감지**는 확장자뿐 아니라 구조도 봅니다(`detectFormat`). `.har` 이름은 HAR로, `.log` / `.jsonl`은 액세스 로그로 강제하고, 그렇지 않으면 문서를 파싱해 키를 살핍니다. OpenAPI 표지(`openapi` / `swagger` / `paths`)를 먼저 확인하고, 그다음 HAR의 `log.entries`를 보며, 마지막으로 첫 줄이 로그 레코드로 파싱되면 액세스 로그로 봅니다. 덕분에 진짜 브라우저 HAR이 `.har` 이름 없이도, 로그가 `.log` 이름 없이도 올바르게 가져와집니다.

**여정 순서 휴리스틱.** OpenAPI 임포터는 가져온 단계를 명세 순서가 아니라 그럴듯한 사용자 여정으로 정렬합니다(예: detail 전에 list, write 전에 read). 그래서 결과 flow가 API를 지나는 실제 경로처럼 읽힙니다. 생성된 단계를 검토하고 경로 파라미터와 요청 본문을 채운 뒤 실행하세요.

바로 쓸 수 있는 예제는 [`examples/imports/`](../examples/imports)에 있습니다. `shop.openapi.yaml`, `shop.openapi`의 HAR 짝인 `shop-session.har`, 학습용 트래픽 샘플 `shop-access.log`, 그리고 `ticketing.openapi.yaml`입니다. 이들은 `http://localhost:9000`을 대상으로 하여 번들된 `server/examples/sample-api`와 맞물립니다.

### 액세스 로그에서 그래프 학습하기

OpenAPI는 *어떤* 엔드포인트가 있는지만 알고, HAR는 한 세션의 *한* 경로만 압니다. **액세스 로그**는 실제 사용자 전부가 실제로 움직인 기록이므로, tmula는 거기서 행동 그래프 자체를 **학습**합니다. 결과물은 관찰된 트래픽의 미니어처입니다: 그것을 스테이징에 재생하면 실제 트래픽 패턴이 깨뜨리는 지점을 배포 전에 보게 됩니다.

- **입력.** Apache/nginx **combined 형식** 또는 **JSON lines**(한 줄에 객체 하나; `time`/`ts`/`timestamp`, `method`+`path` 또는 `request`, `remote_addr`, `user_agent` 등 흔한 키 철자를 자동 인식).
- **세션화.** 클라이언트(IP + User-Agent)별로 묶고 30분 유휴 갭에서 방문을 나눕니다.
- **엔드포인트 병합.** 쿼리를 떼고 가변 세그먼트(숫자, UUID, 긴 16진 id)를 `{id}`로 접어 `/product/123`과 `/product/456`이 한 노드가 됩니다. 가장 뜨거운 30개 엔드포인트만 남기고, 접힌 엔드포인트를 지나는 전이는 다리를 놓아 이어집니다(접힌 수는 import 시 알려 줍니다 - 조용히 잘리지 않습니다).
- **학습되는 것.** 전이 빈도 → 엣지 가중치, 세션 종료 → `exit` 엣지, 가장 흔한 첫 요청 → 시작 노드, 단계 간 간격의 사분위 → think time, 세션 도착률 → `open` 제안, p95 세션 길이 → maxSteps.
- **러너블 우선.** 로그에는 본문이 없으므로 각 템플릿 경로는 그 엔드포인트에서 *가장 많이 관찰된 실제 경로*(예: `/product?id=1023`)를 씁니다. 생성된 시나리오는 그대로 실행 가능하고, 본문/헤더는 나중에 채우면 됩니다.

학습 결과는 선형 flow가 아니라 [graph-first 시나리오 파일](#graph-first-시나리오-파일)입니다 - 분기야말로 학습된 정보이기 때문입니다. 웹 콘솔의 Import에 로그를 붙여 넣으면 그래프 + 템플릿 편집기가 곧바로 채워집니다.

---

## 인증이 필요한 실행

시뮬레이션 트래픽이 진짜 인증 정보를 운반하게 하려면 **자격 증명 풀**(credential pool)을 붙이세요. 각 클로즈드 가상 사용자(**사용자 인덱스** 기준) 또는 오픈 세션(**세션/도착 인덱스** 기준)에 자격 증명이 배정되며, 항목보다 사용자가 많으면 돌아가며 재사용합니다. 템플릿 헤더에서 `"Authorization": "Bearer {{.token}}"`로 참조하거나, 비민감 주체에는 `{{.subject}}`를 쓰세요.

가장 쉬운 방법은 시나리오 파일의 `auth:` 블록입니다.

```yaml
auth:
  strategy: pool          # only "pool" (pre-supplied entries) is supported on this path
  users:
    - { subject: alice, token: jwt-aaa }
    - { subject: bob,   token: jwt-bbb }
```

```bash
tmula run examples/shop/scenario.yaml --users 50   # with the auth: block above
```

**비밀이 in-process에만 머무는 이유.** 도메인 `Credential.Secret` 필드에는 `json:"-"`가 붙어 있어 비밀이 **직렬화되지 않습니다**. HTTP/SSE/저장소 와이어를 건널 수 없고 영속화되지도 않습니다(`String()`도 로그에서 비밀을 가립니다). 구체적으로 다음과 같습니다.

- `auth:` 블록이 있는 `tmula run`은 비밀이 마샬링될 일이 없도록 컨트롤 플레인을 **in-process**로(자체 Go API를 통해) 실행합니다. 비인증 경로는 동등성을 위해 여전히 HTTP로 실제 루프백 엔진을 부팅합니다.
- HTTP로 POST 하려는 `users[].cred`는 **조용히 무시됩니다**. 비밀이 `json:"-"`로 제거되므로 HTTP 제출은 인증을 운반할 수 없습니다.
- 원격 `--engine`은 인증 실행을 **거부합니다**: `a credential pool is not supported against a remote --engine (the secret cannot cross the wire); run in-process to authenticate`.

**제약**(검증 기준): 현재 실행 경로에서는 미리 공급된 `pool` 전략만 동작합니다. `bootstrap-signup`은 "not yet supported via this run path (follow-up)" 메시지로 거부됩니다. 자격 증명 풀은 분산 워커와 **결합할 수 없습니다**(워커 팬아웃이 자체 비인증 사용자를 합성하기 때문).

---

## 분산 모드

아주 큰 실행에서는 여러 머신으로 팬아웃할 수 있습니다. 한 프로세스는 **master**(컨트롤 플레인 + UI 제공)이고, 나머지는 **worker**(gRPC 서비스 제공)입니다. master는 각 worker에 다이얼링하여 가상 사용자를 샤드로 나누고, 그들이 스트리밍한 결과를 로컬 경로와 동일하게 집계합니다.

```bash
# on each worker box
tmula --role worker --addr :9101

# on the master, naming the worker pool
tmula --role master --addr :8080 --workers 10.0.0.5:9101,10.0.0.6:9101
```

RunSpec의 `workers` 필드(또는 콘솔의 **Workers** 필드)로 실험별 워커를 설정할 수도 있습니다.

**`aggregateWorkers`와 정밀도 맞바꿈.** 기본적으로 각 worker는 모든 요청을 다시 스트리밍하고, master는 실행 길이 기반 availability 감지로 엔드포인트별 finding을 분류합니다(완전 정밀도). `aggregateWorkers: true`이면 각 worker가 자기 샤드 전체를 작은 **Summary**(카운터 + 메모리 제한 지연 히스토그램)로 접고 master가 그것들을 병합합니다. 수백만 요청에서도 네트워크와 메모리가 제한되지만, finding이 **실행 전체·종류별**이 됩니다(작동한 종류당 finding 하나, 엔드포인트별 분해 없음, 연속 실패 구간 없음). 요청량이 스트리밍을 압도할 때 쓰고, 엔드포인트별·실행 길이 finding을 원하면 끄세요.

**언제 분산 모드를 써야 하나:** 단일 머신이 필요한 부하를 생성할 수 없을 때(그리고 SUT가 그 부하를 감당할 수 있을 때)만 쓰세요. 오픈 워크로드 모델은 **in-process 전용**입니다. 오픈 실행에 대해 분산 워커는 거부되고(`api: distributed workers are not supported with the open workload model`), 자격 증명 풀도 워커와 함께 거부됩니다.

---

## 안전장치

tmula는 일부러 트래픽을 집중시키므로, 오발이 곧 자초한 장애가 됩니다. 세 가지 가드(`server/internal/safety`)가 그런 일이 우연히 일어나기 어렵게 만들고, 모든 외부 요청은 세 가지를 모두 통과합니다.

- **Allowlist.** 호스트 허용 목록(`TargetEnv.Allowlist`)으로, 실행이 닿아도 되는 유일한 호스트들입니다. 목록에 없는 호스트로 가는 요청은 차단됩니다(`safety: host "<h>" not in allowlist`). 패턴은 정확히 일치하거나 선행 `*.` 와일드카드(`*.example.com`)입니다. allowlist는 비어 있으면 안 됩니다. "아무 데나 닿기" 모드는 없습니다.
- **Rate cap.** 강한 상한입니다. `rateCap.maxRps`(토큰 버킷, 버스트는 1초치 비율로 제한)와 `rateCap.maxConcurrency`(진행 중 상한)가 있고, 둘 다 `> 0`이어야 합니다. 둘 중 하나를 초과하면 대상을 덮치는 대신 그 요청에 `LimitError`가 납니다.
- **Kill switch.** 항상 켜진 **수동** 정지(콘솔의 **Kill run** 버튼)와, 선택적 **자동** 작동이 있습니다. 최근 N개 결과에 대한 *롤링* 오류율이 임계값을 넘으면 가드가 스스로 작동합니다(`auto: rolling error rate X over last N exceeded threshold Y`). 자동 작동은 포화를 실제로 관찰할 수 있도록 **기본 비활성화**입니다.
- **환경 클래스.** `envClass`는 `dev`, `staging`, `prod-locked`입니다. `prod-locked` 대상은 명시적으로 잠금 해제되지 않는 한 거부됩니다(`safety: target env is prod-locked; explicit unlock required (policy §1)`).

이들을 합치면, 실행은 적어 두지 않은 호스트에 닿을 수 없고, 설정한 비율/동시 실행을 넘을 수 없으며, 언제나 멈출 수 있고, 운영(production)을 실수로 칠 수 없습니다.

---

## 예제 도메인

실행 가능한 두 데모는 *일부러 심어 둔 버그*의 카탈로그 역할도 합니다. 그래서 tmula를 거기에 겨눴을 때 어떤 finding을 기대해야 하는지 알 수 있습니다. 웹 **프리셋**으로 하나 고르거나(시나리오 *와* 대상을 모두 채웁니다) CLI에서 실행하세요.

### shop - `server/examples/sample-api` (`:9000`)

여정: `browse → search / category → product → cart → checkout → done`. 심어 둔 버그:

| Endpoint | 심어 둔 동작 | 기대되는 finding |
|----------|--------------|------------------|
| `GET /browse` | 정상, ~3 ms | 없음 |
| `GET /search` | ~5%의 응답이 ~180 ms 잠듦(지연 꼬리) | **p95/p99**에 나타남(게이트 설정 시 threshold) |
| `GET /category` | 정상, ~5 ms | 없음 |
| `GET /product` | ~2%가 **404** 반환(드물게 깨진 상품 링크) | **contract** |
| `POST /cart` | ~8%가 **500** 반환(간헐적 "cart hiccup") | **contract** |
| `POST /checkout` | ~8% 기본 실패가 **동시 부하에 따라 상승**, 40%에서 상한(503). 압박 아래 성능 저하, 완전히 죽지는 않음, 부하가 풀리면 회복 | **contract** + 부하 시 상승하는 **threshold** 오류율 |

### ticketing - `server/examples/ticketing-api` (`:9100`)

여정: `events → detail → seats → hold → pay → done`. 심어 둔 버그:

| Endpoint | 심어 둔 동작 | 기대되는 finding |
|----------|--------------|------------------|
| `GET /events` | 정상, 빠름 | 없음 |
| `GET /events/{id}` | ~3%가 **404** 반환(매진 / 삭제됨) | **contract** |
| `GET /seats` | ~6% 느림(~150 ms, 인기 공연의 지연 꼬리) | **p95/p99** |
| `POST /hold` | ~18%가 **409** 반환(좌석 경쟁, 다른 구매자가 먼저 잡음) | **contract** |
| `POST /pay` | 예매 오픈 혼잡 아래 성능 저하; 동시 부하에 따라 실패 상승, 40%에서 상한(503) | **contract** + 상승하는 **threshold** 오류율 |

`hold`의 `409`와 `pay`의 `503`은 그 특정 엔드포인트에 집중됩니다. 진짜 심어 둔 버그의 패턴입니다(엔드포인트 집중 오류에 대한 FAQ 참고).

처음부터 끝까지 손으로 따라 하는 "0 to 100" 안내(한국어)는 [`examples/USAGE.md`](../examples/USAGE.md)에 있습니다. 이 레퍼런스의 동반 문서이지 중복이 아닙니다.

---

## 문제 해결 & FAQ

**Q: 콘솔이 비어 있거나 "run `make web`" 플레이스홀더 페이지가 보입니다.**
UI를 내장하지 않고 빌드했습니다. 그냥 `make build` / `go build`는 플레이스홀더를 내보냅니다. `make web`을 실행하거나(또는 이미 진짜 콘솔을 내장한 Docker 이미지 / 미리 빌드된 바이너리를 쓰세요) <http://localhost:8080>을 새로고침하세요.

**Q: 실행이 "전부 오류"입니다. 모든 요청이 실패했습니다.**
대상 호스트가 **Allowlist**에 없습니다. 웹 콘솔은 Base URL 호스트를 allowlist에 자동으로 추가하지 **않으며**(`buildRunSpec`은 필드를 트림/분리만 합니다), 안전 가드가 목록에 없는 것은 모두 차단합니다. 대상 호스트를 Base URL과 Allowlist *양쪽 모두*에 넣으세요. (시나리오 파일과 CLI 경로는 allowlist를 대상 호스트로 기본 설정합니다. 두 번 입력하게 만드는 건 웹 콘솔뿐입니다.)

**Q: Docker에서도 실행이 API에 닿지 못합니다.**
Compose 네트워크 안에서 엔진은 SUT를 `localhost`가 아니라 **서비스 이름**으로 찾습니다. Base URL을 `http://sample-api:9000`(shop) 또는 `http://ticketing-api:9100`(ticketing)으로 설정하고 `sample-api` / `ticketing-api`를 Allowlist에 넣으세요. 엔진 컨테이너 안의 `localhost`는 SUT가 아니라 엔진 자신을 가리킵니다.

**Q: `decode: http: request body too large`가 납니다.**
거대한 실행에 가상 사용자당 객체 하나를 보내서 요청 본문이 서버 크기 한도를 넘었습니다. 사용자별 객체를 만들지 마세요: 클로즈드 실행에서는 빈 `users: []`와 `userCount`를 보내거나(서버가 `u0..uN-1`을 합성), 오픈 모델을 쓰세요(도착률에서 자체 세션을 생성). 웹 콘솔은 이미 이렇게 처리합니다.

**Q: 오픈 실행에서 `virtualUserCount must be > 0`이 나는데, 오픈 모델은 사용자 수를 안 쓰지 않나요?**
맞습니다, *명목상* 필드이지만 워크로드 모델과 무관하게 여전히 `> 0`으로 검증됩니다. 아무 양수나 설정하세요(시나리오 파일 경로는 자동으로 `1`로 설정합니다). 오픈 모델 동작을 바꾸지는 않습니다.

**Q: 거대한 오류율, 이봉형 지연(p50 ≈ 0, p95는 초 단위), 그리고 많은 타임아웃. 머신이 고장 난 건가요?**
아닙니다. **과부하 / 포화**의 신호이고, 보통 **동시 실행 상한 없이 같은 머신에서** 부하를 *생성*하면서 SUT를 *서빙*하기 때문입니다. 빠른 성공이 p50 ≈ 0을 만들고, 포화된 꼬리가 p95를 초 단위로 밀어 올리며, 타임아웃이 쌓입니다. **Max concurrency**(오픈 모델)를 설정하거나 **Arrival rate**를 낮추거나, 테스트 대상 시스템을 별도 머신으로 옮겨 해결하세요. SUT의 버그가 아니라 하니스의 측정 아티팩트입니다.

**Q: 오류가 특정 몇몇 엔드포인트에 집중됩니다.**
보통 테스트 대상 시스템의 *진짜* 버그이고, 예제 도메인에서는 일부러 심어 둔 것입니다. ticketing의 `POST /hold` 409(좌석 경쟁)와 `POST /pay` 503(혼잡 시 결제), 또는 shop의 `POST /cart` 500과 `POST /checkout` 성능 저하가 바로 tmula가 드러내려고 만든 엔드포인트 집중 실패입니다. 엔드포인트 집중 오류라면 그 엔드포인트를 보고, 포화 증상을 동반한 실행 전체 오류라면 하니스를 보세요(앞 질문).

**Q: 통과를 기대했는데 CI 실행이 2로 종료되었습니다.**
`--fail-on-findings`(또는 `--fail-on-severity`)는 finding이 감지되면 일부러 `2`로 종료합니다. 게이트가 작동하는 것입니다. 대신 `1`이 났다면 실제 오류이거나 failed/killed 실행(예: 타임아웃이나 kill-switch 작동)이며, 이는 finding과 무관하게 항상 비정상 종료합니다. `0`은 깨끗함을 뜻합니다.

**Q: 원격 `--engine`에 대한 인증 실행이 "동작"하는데 토큰을 안 싣습니다.**
실을 수 없고, 실행 경로가 이를 거부합니다. 원격 `--engine`에 대한 자격 증명 풀은 비밀이 와이어를 건널 수 없어(`json:"-"`) 거부됩니다. 인증하려면 in-process로 실행하세요(`--engine` 없이 `auth:` 블록과 함께 `tmula run`).
