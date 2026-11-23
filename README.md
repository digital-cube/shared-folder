# shared-folder

Deljeni folder za mali tim. Kopija na serveru je zajednička istina; klijenti su
iza NAT-a i sinhronizuju se preko servera. Server je `shfd`, klijent `shf`
(radna imena).

Specifikacija prve verzije: [docs/specifikacija.md](docs/specifikacija.md).

## Status

Postoje zajednički paketi i jezgro servera `shfd`: folderi, članovi sa
tokenima, dnevnik promena, kontrolni HTTP API, pravila konflikta i trash.
Fajlovi se prenose rsync-om preko SSH-a, sa prinudnom komandom
`shfd rsync-gate` po članu ([docs/server-ssh.md](docs/server-ssh.md)).
Klijent `shf` se pridružuje folderu i sinhronizuje ga ručno (`shf sync`) ili u
pozadini (`shf daemon`): server javlja nove promene preko WebSocket-a, a
klijent prati lokalni folder. Zaštita od masovnog brisanja zadržava brisanja
dok ih korisnik ne potvrdi.

| Paket | Šta radi | Odeljak specifikacije |
|---|---|---|
| `internal/pathx` | normalizacija i provera putanja (NFC, `/`, Windows-bezbedna imena), `FoldKey` za poređenje bez veličine slova, imena konflikt i trash fajlova, provera korisničkog imena | 6 „Imena“, 3 „Trash“ |
| `internal/hashx` | SHA-256 celog fajla, strimovano, hex malim slovima | 5 |
| `internal/ignore` | `.shfignore` sa semantikom `.gitignore`, ugnježdeni fajlovi, ugrađena lista i privremeni fajlovi klijenta | 7 |
| `internal/server` | `registry.db`, folderi, `service.db` sa numerisanim migracijama, članovi, pravila iz odeljka 6, trash, `service.log`, žurnal koraka na disku za oporavak posle pada | 3, 6 |
| `internal/server/httpapi` | kontrolni API: `changes`, `uploads`, `commit`, `ops`, `sync-report`, `ssh-key`, WebSocket `ws` (`{"head": N}`, ping/pong, opoziv zatvara vezu) | 4 |
| `internal/transport` | interfejs `Transport` i lokalna implementacija (testovi) | 5 |
| `internal/transport/rsyncssh` | `Transport` preko sistemskog `rsync`-a i `ssh`-a: nastavljanje prekinutog uploada, download u `.shf-tmp-*` i rename, provera hash-a, ključ servera u direktorijumu stanja | 5 |
| `internal/rsyncgate` | prinudna komanda `shfd rsync-gate`: propušta samo upis u `tmp/<upload_id>` i čitanje iz `storage/` | 5 |
| `internal/authkeys` | blok članova u `authorized_keys` (dodavanje, opoziv, `keys sync`) | 5 |
| `internal/shfdcli` | komande `shfd` | 8.5 |
| `internal/client` | klijent: lokalno stanje (`state.db`, manifest, stanje servera), skeniranje uz `.shfignore`, poređenje tri stanja (`Plan`), ciklus sinhronizacije, `join`, `status`, zaštita od masovnog brisanja, `settings.json` | 7, 8.1, 8.2, 8.4 |
| `internal/daemon` | `shf daemon`: WebSocket sa ponovnim povezivanjem, rezervni poll, praćenje foldera (fsnotify), odlaganje, fajlovi koji se još upisuju, puno poređenje, sopstvena preuzimanja se ne šalju nazad | 8.3 |
| `internal/service` | `shf service install\|uninstall\|status`: systemd user jedinica, launchd agent | 8.3 |
| `internal/shfcli` | komande `shf` | 8.5 |
| `internal/sshdtest` | privremeni `sshd` za integracione testove | — |

## Server

```sh
export SHFD_DATA_ROOT=/var/lib/shfd        # ili --data-root uz svaku komandu
shfd share create tim [--admin ana]        # ispisuje share-id i token prvog admina
shfd member add <share-id> bob [--admin]   # ispisuje token člana (samo tada)
shfd member revoke <share-id> bob
shfd member list <share-id>
shfd share list
shfd keys sync                             # blok u authorized_keys iz baza (uz --authorized-keys)
shfd serve --listen :8443 [--tls-cert cert.pem --tls-key key.pem] [--ssh-endpoint shfd@host:22]
```

Bez `--tls-cert`/`--tls-key` server govori običan HTTP i očekuje TLS na
reverse proxy-ju. Kodovi grešaka API-ja su u `internal/server/errors.go`.
Za prenos fajlova: sistemski korisnik, `sshd` i `SHFD_AUTHORIZED_KEYS` su
opisani u [docs/server-ssh.md](docs/server-ssh.md).

## Klijent

```sh
shf join https://shf.example.org <share-id> <token> ~/Tim   # pridruživanje i prvi sync
shf sync   [<folder>]    # jedan ciklus; izlazni kod 1 ako nešto nije uspelo
shf status [<folder>]    # poslednji sync, seq i HEAD, promene koje čekaju, problemi
shf log    [<folder>]    # poslednji ciklusi i šta je u njima urađeno
```

`join` u folder koji već ima fajlove samo spaja (ništa se ne briše). Stanje
foldera je van njega, u `$XDG_STATE_HOME/shf/<share-id>/` (na macOS-u
`~/Library/Application Support/shf/`); `SHF_STATE_HOME` menja koren. Za
prenos su potrebni `ssh` i rsync 3.2.4+; adresu sshd-a klijent dobija od
servera (`shfd serve --ssh-endpoint`) ili iz `--ssh`.

### Pozadinski klijent

```sh
shf daemon                 # u prvom planu; Ctrl-C / SIGTERM završava tekuću stavku i izlazi
shf service install        # systemd user jedinica (Linux) ili launchd agent (macOS), odmah i pri svakom prijavljivanju
shf service status         # da li radi, poslednje linije loga
shf service uninstall
```

Jedan proces služi sve foldere kojima se korisnik pridružio (`shf join`);
folder pridružen dok proces radi preuzima se u roku od jednog intervala
poll-a. Na Linux-u je jedinica `~/.config/systemd/user/shf.service`, log se
čita sa `journalctl --user -u shf`; na macOS-u je agent
`~/Library/LaunchAgents/rs.digitalcube.shf.plist`, a log
`~/Library/Logs/shf.log`. Ako je pri instalaciji postavljen
`SHF_STATE_HOME`, upisuje se u jedinicu. Posle zamene binarnog fajla
ponovo pokrenite `shf service install`.

Šta pozadinski klijent radi za svaki folder:

- drži WebSocket ka serveru (`GET /v1/shares/{id}/ws`) i pokreće ciklus kad
  HEAD servera pređe lokalni seq; kad veza pukne, ponovo se povezuje sa
  rastućim razmakom (1 s, 2 s, … do 60 s) i slučajnim odstupanjem;
- dok WebSocket ne radi, pita server za HEAD na 60 s;
- prati folder (inotify na Linux-u, kqueue na macOS-u, preko fsnotify-a),
  uz `.shfignore`, i pokreće ciklus 2 s posle poslednje promene;
- fajl šalje tek kad mu veličina i mtime miruju 3 s;
- pri pokretanju i jednom u 24 h radi puno poređenje sa serverom;
- ciklusi se ne preklapaju; ono što stigne tokom ciklusa pokreće sledeći;
- promene koje sam upiše tokom preuzimanja ne šalje nazad.

Intervali i pragovi su u `$SHF_STATE_HOME/settings.json` (podrazumevano
`~/.local/state/shf/settings.json`); ključ koji nedostaje zadržava
podrazumevanu vrednost:

```json
{
  "debounce": "2s",
  "settle": "3s",
  "poll": "60s",
  "full_sync": "24h",
  "reconnect_max": "60s",
  "ping": "30s",
  "retry_max": "60s",
  "mass_delete": {"files": 20, "percent": 10}
}
```

### Zaštita od masovnog brisanja

Kad bi ciklus serveru poslao brisanje više od `mass_delete.files` fajlova
ili više od `mass_delete.percent` % fajlova foldera (šta god je manje),
ne šalje se nijedno brisanje iz tog ciklusa; novi i izmenjeni fajlovi i
preuzimanja idu normalno. Folder je tada `blocked` i svako dalje brisanje
čeka zajedno sa ostalima; to se upisuje u lokalni log (`shf log`).

```sh
shf status          # state: blocked i spisak brisanja koja čekaju
shf confirm         # šalje ih (sačeka ciklus pozadinskog klijenta ako je u toku)
shf restore-local   # vraća te fajlove sa servera i skida blokadu
```

Negativna vrednost isključuje taj prag. Pažnja: po specifikaciji važi
manji prag, pa u folderu sa manje od 10 fajlova i jedno brisanje blokira
(10 % od 9 je 0,9). Ako koren foldera ne postoji ili se ne može čitati
(isključen disk), klijent ne radi ništa i javlja grešku; to se nikad ne
tumači kao brisanje.

## Raspored

```
cmd/shfd/        server
cmd/shf/         klijent
internal/        zajednički kod
docs/            specifikacija, postavljanje servera
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

Integracioni testovi u `internal/transport/rsyncssh` pokreću pravi `sshd` na
slobodnom portu, sa privremenom konfiguracijom i `authorized_keys`, i kroz njega
rade upload, download i prekinut upload koji se nastavlja. Preskaču se kad
`rsync`, `ssh`, `ssh-keygen` ili `sshd` nisu instalirani, i uz `go test -short`.

Testovi klijenta (`internal/client`) pokreću pravi `shfd` u procesu sa dva
klijenta u privremenim folderima i lokalnim transportom; `TestJoinOverSSH`
radi `join` i sync preko pravog rsync-a i `sshd`-a (preskače se kao gore).

Testovi pozadinskog klijenta (`internal/daemon`) pokreću dva `shf daemon`-a
nad pravim `shfd`-om u procesu i rade u realnom vremenu (oko 7 s, paralelno):
fajl stiže drugom klijentu bez komande (sa podrazumevanim intervalima, u roku
od 10 s), rezervni poll bez WebSocket-a, ponovno povezivanje posle prekida,
fajl koji se polako upisuje šalje se jednom, preuzimanja se ne šalju nazad.

Test `internal/ignore.TestGitCompat` poredi matcher sa samim `git`-om na
nasumičnim stablima; preskače se ako `git` nije instaliran ili uz `go test -short`.
