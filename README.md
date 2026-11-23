# shared-folder

Deljeni folder za mali tim. Kopija na serveru je zajednička istina; klijenti su
iza NAT-a i sinhronizuju se preko servera. Server je `shfd`, klijent `shf`
(radna imena).

Specifikacija prve verzije: [docs/specifikacija.md](docs/specifikacija.md).

## Status

Postoje zajednički paketi i jezgro servera `shfd`: folderi, članovi sa
tokenima, dnevnik promena, kontrolni HTTP API, pravila konflikta i trash.
Klijent `shf` za sada ispisuje samo verziju i pomoć.

| Paket | Šta radi | Odeljak specifikacije |
|---|---|---|
| `internal/pathx` | normalizacija i provera putanja (NFC, `/`, Windows-bezbedna imena), `FoldKey` za poređenje bez veličine slova, imena konflikt i trash fajlova, provera korisničkog imena | 6 „Imena“, 3 „Trash“ |
| `internal/hashx` | SHA-256 celog fajla, strimovano, hex malim slovima | 5 |
| `internal/ignore` | `.shfignore` sa semantikom `.gitignore`, ugnježdeni fajlovi, ugrađena lista i privremeni fajlovi klijenta | 7 |
| `internal/server` | `registry.db`, folderi, `service.db` sa numerisanim migracijama, članovi, pravila iz odeljka 6, trash, `service.log`, žurnal koraka na disku za oporavak posle pada | 3, 6 |
| `internal/server/httpapi` | kontrolni API: `changes`, `uploads`, `commit`, `ops`, `sync-report` | 4 |
| `internal/transport` | interfejs `Transport` i lokalna implementacija (testovi) | 5 |
| `internal/shfdcli` | komande `shfd` | 8.5 |

## Server

```sh
export SHFD_DATA_ROOT=/var/lib/shfd        # ili --data-root uz svaku komandu
shfd share create tim [--admin ana]        # ispisuje share-id i token prvog admina
shfd member add <share-id> bob [--admin]   # ispisuje token člana (samo tada)
shfd member revoke <share-id> bob
shfd member list <share-id>
shfd share list
shfd serve --listen :8443 [--tls-cert cert.pem --tls-key key.pem]
```

Bez `--tls-cert`/`--tls-key` server govori običan HTTP i očekuje TLS na
reverse proxy-ju. Kodovi grešaka API-ja su u `internal/server/errors.go`.

## Raspored

```
cmd/shfd/        server
cmd/shf/         klijent
internal/        zajednički kod
docs/            specifikacija
```

## Build i testovi

Potreban je Go 1.27 ili noviji.

```sh
make build   # bin/shfd i bin/shf
make test    # go test ./...
make lint    # go vet + gofmt -l (i golangci-lint ako je instaliran)
make check   # lint + test + build
```

`./bin/shfd version`, `./bin/shf help`.

Test `internal/ignore.TestGitCompat` poredi matcher sa samim `git`-om na
nasumičnim stablima; preskače se ako `git` nije instaliran ili uz `go test -short`.
