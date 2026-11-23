# shared-folder

Deljeni folder za mali tim. Kopija na serveru je zajednička istina; klijenti su
iza NAT-a i sinhronizuju se preko servera. Server je `shfd`, klijent `shf`
(radna imena).

Specifikacija prve verzije: [docs/specifikacija.md](docs/specifikacija.md).

## Status

Postavljena je osnova: Go modul, dva binarna fajla koji za sada ispisuju samo
verziju i pomoć, i zajednički paketi koje koriste i server i klijent.

| Paket | Šta radi | Odeljak specifikacije |
|---|---|---|
| `internal/pathx` | normalizacija i provera putanja (NFC, `/`, Windows-bezbedna imena), `FoldKey` za poređenje bez veličine slova, imena konflikt i trash fajlova, provera korisničkog imena | 6 „Imena“, 3 „Trash“ |
| `internal/hashx` | SHA-256 celog fajla, strimovano, hex malim slovima | 5 |
| `internal/ignore` | `.shfignore` sa semantikom `.gitignore`, ugnježdeni fajlovi, ugrađena lista i privremeni fajlovi klijenta | 7 |

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
