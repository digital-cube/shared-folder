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
dok ih korisnik ne potvrdi. Admin na serveru pregleda, vraća i čisti trash,
proverava `storage/` prema bazi i gleda statistike foldera
([docs/admin.md](docs/admin.md)). Testovi s kraja na kraj (`make e2e`)
proveravaju sve zajedno: pravi `shfd`, dva ili tri `shf daemon`-a i rsync
preko SSH-a.

| Paket | Šta radi | Odeljak specifikacije |
|---|---|---|
| `internal/pathx` | normalizacija i provera putanja (NFC, `/`, Windows-bezbedna imena), `FoldKey` za poređenje bez veličine slova, imena konflikt, trash i `restored` fajlova, provera korisničkog imena | 6 „Imena“, 3 „Trash“ |
| `internal/hashx` | SHA-256 celog fajla, strimovano, hex malim slovima | 5 |
| `internal/ignore` | `.shfignore` sa semantikom `.gitignore`, ugnježdeni fajlovi, ugrađena lista i privremeni fajlovi klijenta | 7 |
| `internal/server` | `registry.db`, folderi, `service.db` sa numerisanim migracijama, članovi, pravila iz odeljka 6, trash (lista, vraćanje, čišćenje), `verify [--repair]`, statistike, `service.log`, žurnal koraka na disku za oporavak posle pada | 3, 4, 6 |
| `internal/server/httpapi` | kontrolni API: `changes`, `uploads`, `commit`, `ops`, `sync-report`, `ssh-key`, WebSocket `ws` (`{"head": N}`, ping/pong, opoziv zatvara vezu), admin pozivi (`admin/trash`, `admin/verify`, `admin/stats`, samo uloga `admin`) i lokalni admin socket | 4 |
| `internal/transport` | interfejs `Transport` i lokalna implementacija (testovi) | 5 |
| `internal/transport/rsyncssh` | `Transport` preko sistemskog `rsync`-a i `ssh`-a: nastavljanje prekinutog uploada i downloada (i posle ponovnog pokretanja klijenta), download u `.shf-tmp-part-<hash>` i rename, provera hash-a, ključ servera u direktorijumu stanja | 5 |
| `internal/rsyncgate` | prinudna komanda `shfd rsync-gate`: propušta samo upis u `tmp/<upload_id>` i čitanje iz `storage/` | 5 |
| `internal/authkeys` | blok članova u `authorized_keys` (dodavanje, opoziv, `keys sync`) | 5 |
| `internal/shfdcli` | komande `shfd`; admin komande idu kroz `admin.sock` pokrenutog servera ili, bez njega, kroz isti handler u procesu | 8.5 |
| `internal/client` | klijent: lokalno stanje (`state.db`, manifest, stanje servera), skeniranje uz `.shfignore`, poređenje tri stanja (`Plan`), ciklus sinhronizacije, `join`, `status`, zaštita od masovnog brisanja, `settings.json` | 7, 8.1, 8.2, 8.4 |
| `internal/daemon` | `shf daemon`: WebSocket sa ponovnim povezivanjem, rezervni poll, praćenje foldera (fsnotify), odlaganje, fajlovi koji se još upisuju, puno poređenje, sopstvena preuzimanja se ne šalju nazad | 8.3 |
| `internal/service` | `shf service install\|uninstall\|status`: systemd user jedinica, launchd agent; pamti putanje `rsync`-a i `ssh`-a i postavlja `PATH`, odbija star rsync | 8.3 |
| `internal/shfcli` | komande `shf` | 8.5 |
| `internal/sshdtest` | privremeni `sshd` za integracione testove | — |
| `e2e` | testovi s kraja na kraj (build tag `e2e`, `make e2e`): pravi `shfd`, `shf daemon`-i i rsync preko SSH-a | 6, 7, 8 |

## Server

```sh
export SHFD_DATA_ROOT=/var/lib/shfd        # ili --data-root uz svaku komandu
export SHFD_DB_ROOT=/var/lib/shfd/db       # ili --db-root; baze na lokalnom disku (podrazumevano: data-root)
shfd share create tim [--admin ana]        # ispisuje share-id i token prvog admina
shfd member add <share-id> bob [--admin]   # ispisuje token člana (samo tada); opozvanog bob-a vraća
shfd member revoke <share-id> bob
shfd member set-role <share-id> bob admin|member   # ne oduzima ulogu poslednjem adminu (last_admin)
shfd member reset-token <share-id> bob     # nov token aktivnom članu; stari odmah prestaje, SSH ključ ostaje
shfd member list <share-id>                # i otisak trenutnog SSH ključa (uređaj koji je aktivan)
shfd share list
shfd keys sync                             # blok u authorized_keys iz baza (uz --authorized-keys)
shfd serve --listen :8443 [--tls-cert cert.pem --tls-key key.pem] [--ssh-endpoint shfd@host:22]

shfd trash list <share-id> [--path P] [--by U] [--reason deleted|replaced] [--since 2026-11-01]
shfd trash restore <share-id> <trash-id> [--to <putanja>]
shfd trash purge <share-id> (--older-than 90d | --id N | --all) [--dry-run] [--yes]
shfd verify <share-id> [--repair] [--json]   # izlazni kod 1 ako nađe neslaganje
shfd stats <share-id> [--json]
shfd db move --to /var/lib/shfd/db           # baze na novo mesto, dok server ne radi
```

**Baze na lokalnom disku.** `storage/` (i `tmp/`, `trash/`, `service.log`)
može biti na NFS-u, ali SQLite baze (`registry.db`, `<share-id>/service.db`)
neka budu na lokalnom disku: `--db-root` / `SHFD_DB_ROOT`. SQLite ne
preporučuje WAL i POSIX lock-ove na mrežnom fajl-sistemu, a zaglavljen NFS
zaustavlja pisca foldera. `shfd serve` upozorava (`level=WARN`) kad su baze
na mrežnom disku; postojeće baze premešta `shfd db move`
([docs/server-ssh.md](docs/server-ssh.md#baze-na-lokalnom-disku),
[docs/admin.md](docs/admin.md#premeštanje-baza-shfd-db-move)).

Username ostaje zauzet i kad je član opozvan: opozvanog člana vraća
`shfd member add` sa istim imenom (isti id i istorija, nov token, uloga prema
`--admin`, bez SSH ključa dok ga klijent ne registruje ponovo kroz `shf
join`). Za ime aktivnog člana `member add` vraća `member_exists`.

Admin komande su bezbedne dok `shfd serve` radi: idu kroz njegov lokalni
socket `<db-root>/admin.sock` i istog pisca foldera kao i klijenti. Opis i
primeri: [docs/admin.md](docs/admin.md).

Web GUI (prijava admin tokenom, članovi i tokeni, logovi uživo, fajlovi i
trash, uputstvo za instalaciju) je poseban servis **shfd-gui** na
`https://<host>/gui`: [docs/gui.md](docs/gui.md), build `make gui`.

`GET /healthz` (i `GET /`) bez tokena vraća `{"service":"shfd","version":…,"ok":true}`, a 503 kad registry baza ne odgovara ili kad pisac nekog otvorenog foldera ne odgovori u roku od 2 s (`"error":"share <id> stuck"`).

Bez `--tls-cert`/`--tls-key` server govori običan HTTP i očekuje TLS na
reverse proxy-ju. Kodovi grešaka API-ja su u `internal/server/errors.go`.
Za prenos fajlova: sistemski korisnik, `sshd` i `SHFD_AUTHORIZED_KEYS` su
opisani u [docs/server-ssh.md](docs/server-ssh.md).

## Klijent

Instalacija `shf` (gotov paket ili build): [docs/install.md](docs/install.md).
Na macOS-u posle instalacije preuzetog paketa obavezno skinuti karantin, inače
ga Gatekeeper ne pušta:

```sh
sudo xattr -d com.apple.quarantine "$(command -v shf)"
xattr -l "$(command -v shf)"        # ne sme da ispiše com.apple.quarantine
```

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

`shf` traži `rsync` i `ssh` u `PATH`-u; `SHF_RSYNC` i `SHF_SSH` (apsolutne
putanje) imaju prednost. Na macOS-u je `/usr/bin/rsync` Apple-ov `openrsync`
(predstavlja se kao 2.6.9) i ne radi: `brew install rsync`, pa Homebrew u
`PATH` ispred `/usr/bin`, npr. `eval "$(/opt/homebrew/bin/brew shellenv)"` u
`~/.zprofile` (na Intel Mac-u `/usr/local/bin/brew`). `rsync --version` u
novom terminalu treba da pokaže 3.2.4 ili noviji. Ako nađe pogrešan rsync,
`shf` kaže koji je našao (putanju) i šta treba uraditi.

Ako server ne prihvata SSH ključ ovog uređaja (član je opozvan, ili se isti
član pridružio sa drugog uređaja i taj ključ je zamenio ovaj), `shf status`
pokazuje `state: key refused`, `shf sync` vraća istu poruku, a pozadinski
klijent to upisuje u log jednom i ne pokušava češće od pauze iznad. Rešenje
je `shf join` ponovo, u isti folder, sa sopstvenim tokenom: ključ ovog
uređaja se ponovo registruje, a stanje foldera ostaje. Svaki uređaj treba
da ima svog člana (token); jedan token na dva uređaja znači da radi samo
poslednji koji se pridružio.

**Jedan član = jedan uređaj.** Server čuva jedan SSH ključ po članu. Kad se
član pridruži sa drugog uređaja, `shf join` tamo upozorava:

```
shf: warning: this member was already joined from another device (key SHA256:…); that device can no longer sync. Every device or person needs its own member and its own token (shfd member add on the server)
```

Join se ipak završi, ali prvi uređaj od tada dobija `key refused`. Ponovljen
`join` sa istog uređaja (isti ključ, npr. posle prekida) ne upozorava. Za
drugi uređaj ili drugu osobu admin pravi novog člana (`shfd member add`).

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

Servis ne vidi `PATH` vašeg shell-a (launchd daje samo
`/usr/bin:/bin:/usr/sbin:/sbin`), pa `shf service install` **pamti putanje
alata**: nalazi `rsync` i `ssh` isto kao `shf join` (u `PATH`-u shell-a iz
kog ga pokrećete, ili iz `SHF_RSYNC`/`SHF_SSH`), proverava verziju rsync-a i
upisuje apsolutne putanje u jedinicu/agent kao `SHF_RSYNC` i `SHF_SSH`, uz
`PATH` (na macOS-u `/opt/homebrew/bin:/usr/local/bin`, pa `PATH` shell-a,
pa sistemske putanje; na Linux-u `PATH` shell-a). Putanje koje je upisao
ispisuje na kraju. Ako je nađeni rsync stariji od 3.2.4 ili je `openrsync`,
ne instalira ništa, kaže zašto i izlazi sa kodom 1. **Posle zamene ili
nadogradnje rsync-a (ili ssh-a) ponovo pokrenite `shf service install`.**
Na macOS-u se provera radi ovako:

```sh
rsync --version | head -1     # rsync  version 3.x.y (ne openrsync)
shf service install           # na kraju: rsync: /opt/homebrew/bin/rsync
shf service status            # state: running
tail ~/Library/Logs/shf.log   # bez "rsync/ssh not available"
```

Šta pozadinski klijent radi za svaki folder:

- drži WebSocket ka serveru (`GET /v1/shares/{id}/ws`) i pokreće ciklus kad
  HEAD servera pređe lokalni seq; kad veza pukne, ponovo se povezuje sa
  rastućim razmakom (1 s, 2 s, … do 60 s) i slučajnim odstupanjem;
- dok WebSocket ne radi, pita server za HEAD na 60 s;
- prati folder (inotify na Linux-u, kqueue na macOS-u, preko fsnotify-a),
  uz `.shfignore`, i pokreće ciklus 2 s posle poslednje promene;
- fajl šalje tek kad mu veličina i mtime miruju 3 s, a brisanje tek kad
  folder iz kog je fajl nestao miruje 3 s (da premeštanje koje je još u toku
  ne ispadne brisanje i nov fajl);
- pri pokretanju i jednom u 24 h radi puno poređenje sa serverom;
- ciklusi se ne preklapaju; ono što stigne tokom ciklusa pokreće sledeći;
- posle neuspelog ciklusa čeka pre sledećeg: kad server odbije SSH ključ ili
  nije dostupan (i rsync izlazni kod 255) 5 s, 10 s, 20 s … do 5 min, sa
  slučajnim odstupanjem (`transport_retry`, `transport_retry_max`), za
  ostale greške 2 s … `retry_max`. Novi HEAD sa servera ne skraćuje pauzu;
  lokalna promena je skraćuje samo kad ključ nije odbijen, i ne ispod prve
  pauze. Uspešan ciklus vraća pauzu na početak. Svaki pokušaj je prijava na
  sshd servera, a OpenSSH (`PerSourcePenalties`) kažnjava adresu sa koje
  prijave stalno padaju, za sve korisnike iza nje;
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
  "transport_retry": "5s",
  "transport_retry_max": "5m",
  "mass_delete": {"files": 20, "percent": 10},
  "bwlimit": 0
}
```

`bwlimit` ograničava brzinu svakog prenosa u KiB/s (rsync `--bwlimit`), 0 je
bez ograničenja. Prekinut prenos (pukla veza, ugašen ili srušen klijent) se
nastavlja od onoga što je već stiglo: server za istu najavu vraća isti
`upload_id`, a preuzimanje ostaje u `.shf-tmp-part-<hash>` pored odredišta.
Sa `shf daemon -v` svaki prenos se loguje (`msg=transfer … sent=… matched=…`).

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
docs/            specifikacija, instalacija, postavljanje servera, admin komande
```

## Instalacija

Gotovi paketi za Linux x86-64, macOS Intel i macOS Apple Silicon
(`make release`), šta treba na uređaju (rsync 3.2.4+, ssh), korak sa
karantinom na macOS-u (`xattr -d com.apple.quarantine "$(command -v shf)"`)
i build iz izvornog koda po OS-u: [docs/install.md](docs/install.md).

## Build i testovi

Potrebni su **Go 1.27+** (iz `go.mod`), **make** i **git**; za testove još
rsync 3.2.4+, OpenSSH klijent i `sshd`, a za GUI Node 22+ i npm. Kako se to
instalira na Debian/Ubuntu, Fedora, macOS i Windows:
[docs/install.md](docs/install.md#2-build-iz-izvornog-koda).

```sh
make build   # bin/shfd i bin/shf
make release # dist/: paketi za linux-amd64, darwin-amd64, darwin-arm64 i SHA256SUMS
make test    # go test ./...
make lint    # go vet + gofmt -l (i golangci-lint ako je instaliran)
make check   # lint + test + build
make e2e     # testovi s kraja na kraj (nisu deo check-a; vidi dole)
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

### Testovi s kraja na kraj (`make e2e`)

```sh
make e2e                                   # oko 1,5 min, testovi idu paralelno
make e2e E2E_FLAGS='-count=1 -v -run TestConflict'   # jedan scenario
E2E_LOGS=1 make e2e E2E_FLAGS='-count=1 -v'          # logovi i kad test prođe
```

Paket `e2e/` (build tag `e2e`; `make check` ga samo prevodi, ne pokreće)
proverava servis kao celinu: gradi `shfd` i `shf`, a za svaki test pravi
privremeni data-root, pokreće `sshd` na slobodnom portu sa privremenom
konfiguracijom i `authorized_keys`, `shfd serve`, folder i članove, pridružuje
klijente (`shf join`, pravi SSH ključ i rsync) i pokreće `shf daemon` za
svakog, sa skraćenim intervalima iz `settings.json`. Čeka se uslov sa rokom,
nikad fiksni `sleep`. Na kraju svakog scenarija: folder svakog klijenta je
jednak `storage/` (bez putanja koje taj klijent ignoriše), `shfd verify` je
čist i dnevnik nema rupa u rednim brojevima. Kad test padne, ispisuju se
`service.log`, izlaz `shfd serve` i log svakog klijenta. Procesi se gase i
direktorijumi brišu i kad test padne.

Scenariji (`docs/specifikacija.md`, odeljci 6–8): osnovni tok, pregažena
verzija u trash-u, konflikt dok su oba odvojena, isti sadržaj istovremeno,
izmena protiv brisanja (oba redosleda), povratak posle 200 promena,
`.shfignore`, prekinut prenos od 200 MB (upload i download, klijent ubijen na
trećini), masovno brisanje (`confirm` i `restore-local`), pridruživanje u
neprazan folder, opoziv člana (API i SSH), vraćanje opozvanog člana i ponovni
`shf join`, vraćanje iz trash-a i pad servera usred uploada.

Šta treba na mašini (Linux):

- Go iz `go.mod`, `rsync` 3.2.4+, OpenSSH klijent (`ssh`, `ssh-keygen`) i
  server (`sshd`, ne mora da radi kao servis): `apt install rsync
  openssh-client openssh-server`. Bez njih se testovi preskaču sa porukom šta
  nedostaje.
- Ništa od root-a: `sshd` se pokreće kao vi, na `127.0.0.1` i slobodnom
  portu, sa sopstvenim host ključem; vaš `~/.ssh` se ne čita i ne menja.
- Oko 1,5 GB slobodnog mesta u `$TMPDIR` (scenariji sa velikim fajlovima).
