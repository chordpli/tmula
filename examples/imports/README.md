# Import examples

Ready-made API descriptions you can import to scaffold a tmula scenario instead
of hand-writing the behavior graph + templates.

| File | Format | What it is |
|------|--------|------------|
| `shop.openapi.yaml` | OpenAPI 3 | The demo shop API (`examples/sample-api`): browse / search / category / product / cart / checkout. |
| `shop-session.har`  | HAR 1.2   | A recorded shopping session (browse → search → product → cart → checkout). |

## From the web UI

Open the console, go to the **Scenario** card → **Import from OpenAPI / HAR**,
choose one of these files (or paste its contents), and click **Import**. The
scenario graph + API templates fields are filled in for you; review and Run.

## From the CLI

```sh
tmula init --from examples/imports/shop.openapi.yaml --out scenario.yaml
tmula init --from examples/imports/shop-session.har  --out scenario.yaml
```

Both target `http://localhost:9000`, so they line up with the bundled
`examples/sample-api`. Start it first with `go run ./examples/sample-api`.
