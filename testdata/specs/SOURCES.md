# Pinned test specs

Downloaded 2026-11-12. Don't edit these files. To refresh one, download it again,
update this table, and regenerate the goldens (`go test ./internal/openapi -run Golden -update`).

| File | Source | SHA-256 (uncompressed) |
|---|---|---|
| `petstore3.json` | https://petstore3.swagger.io/api/v3/openapi.json | `00441c05add60f28da55eb4563d3fb6bbda364ba8eb7b381e2e2c98613005c82` |
| `github.json.gz` | https://raw.githubusercontent.com/github/rest-api-description/5c36b6f685bae2acccf24a442f3240bf121cb488/descriptions/api.github.com/api.github.com.json | `2a93393cfccb292caeaa0ad9ab250900e361bda28f76743cc812dd0951cae8f0` |
| `stripe.json.gz` | https://raw.githubusercontent.com/stripe/openapi/6763e1a3f199026b0893cf190d95130d3700c045/openapi/spec3.json | `7cff4cc46d0654101a36a3302f7544640f5f73e4f3cad6f264f0e23c19fa1776` |

The `.gz` files are gzip-compressed (`gzip -9 -n`) to keep the repository small.
The golden test checks each file's uncompressed SHA-256 against this table.
