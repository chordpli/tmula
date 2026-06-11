# Import examples

Ready-made inputs you can import to scaffold a tmula scenario instead of
hand-writing the behavior graph + templates.

| File | Format | What it is |
|------|--------|------------|
| `shop.openapi.yaml` | OpenAPI 3 | The demo shop API (`server/examples/sample-api`): browse / search / category / product / cart / checkout. |
| `shop-session.har`  | HAR 1.2   | A recorded shopping session (browse → search → product → cart → checkout). |
| `shop-access.log`   | combined access log | Real-style traffic from five visitors. tmula **learns** the branching behavior graph from it: sessions, transition weights, drop-offs, think time. |

The OpenAPI and HAR imports scaffold a *linear* flow you reorder by hand; the
access-log import goes further and emits the full **graph-first** scenario —
weighted branches learned from how the traffic actually moved.

## From the web UI

Open the console, go to the **Scenario** card → **Import from OpenAPI / HAR**,
choose one of these files (or paste its contents), and click **Import**. The
scenario graph + API templates fields are filled in for you; review and Run.

## From the CLI

```sh
tmula init --from examples/imports/shop.openapi.yaml --out scenario.yaml
tmula init --from examples/imports/shop-session.har  --out scenario.yaml
tmula init --from examples/imports/shop-access.log --target http://localhost:9000 --out scenario.yaml
```

(An access log names no host, so the learner needs `--target`. JSON-lines logs
work too — common key spellings are auto-detected.)

Both target `http://localhost:9000`, so they line up with the bundled
`server/examples/sample-api`. Start it first with `( cd server && go run ./examples/sample-api )`.
