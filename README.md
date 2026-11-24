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
| `internal/password` | lozinke naloga: Argon2id u PHC zapisu, provera u konstantnom vremenu, rehash slabijih parametara, pravila (12–256 znakova, ne username), privremena lozinka | — |
| `internal/server` | `registry.db`, folderi, `service.db` sa numerisanim migracijama, članovi, nalozi (korisnici, uređaji, sesije, ograničenje pokušaja, `user_shares`), pravila iz odeljka 6, trash (lista, vraćanje, čišćenje), `verify [--repair]`, statistike, `service.log`, žurnal koraka na disku za oporavak posle pada | 3, 4, 6 |
| `internal/server/httpapi` | kontrolni API: `changes`, `uploads`, `commit`, `ops`, `sync-report`, `ssh-key`, WebSocket `ws` (`{"head": N}`, ping/pong, opoziv zatvara vezu), prijava i nalozi (`auth/login`, `auth/logout`, `me`, `users`; device token i na rutama foldera; `X-Forwarded-For` samo od `--trusted-proxy`), admin pozivi (`admin/trash`, `admin/verify`, `admin/stats`, `admin/files` sa stanjem sličice, `admin/thumbs/{hash}`, samo uloga `admin`) i lokalni admin socket (uz rute za `shfd thumbs`: `thumbs/jobs`, `thumbs/done`, `thumbs/unreferenced`) | 4 |
| `internal/transport` | interfejs `Transport` i lokalna implementacija (testovi) | 5 |
| `internal/transport/rsyncssh` | `Transport` preko sistemskog `rsync`-a i `ssh`-a: nastavljanje prekinutog uploada i downloada (i posle ponovnog pokretanja klijenta), download u `.shf-tmp-part-<hash>` i rename, provera hash-a, ključ servera u direktorijumu stanja | 5 |
| `internal/rsyncgate` | prinudna komanda `shfd rsync-gate`: propušta samo upis u `tmp/<upload_id>` i čitanje iz `storage/` | 5 |
| `internal/authkeys` | blok članova u `authorized_keys` (dodavanje, opoziv, `keys sync`) | 5 |
| `internal/thumbs` | sličice slika i PDF-ova: dekodiranje u čistom Go-u (EXIF orijentacija), `pdftoppm` sa rokom, veličine `s`/`m`/`l` (96/256/512 px, JPEG), skladište `thumbnails/` po hash-u sadržaja, markeri `.fail` i `.work` | 3 „Thumbnails“ |
| `internal/shfdcli` | komande `shfd`; admin komande idu kroz `admin.sock` pokrenutog servera ili, bez njega, kroz isti handler u procesu | 8.5 |
| `internal/client` | klijent: lokalno stanje (`state.db`, manifest, stanje servera), skeniranje uz `.shfignore`, poređenje tri stanja (`Plan`), ciklus sinhronizacije, `join`, `status`, zaštita od masovnog brisanja, `settings.json` | 7, 8.1, 8.2, 8.4 |
| `internal/daemon` | `shf daemon`: WebSocket sa ponovnim povezivanjem, rezervni poll, praćenje foldera (fsnotify), odlaganje, fajlovi koji se još upisuju, puno poređenje, sopstvena preuzimanja se ne šalju nazad, automatsko ažuriranje između ciklusa | 8.3 |
| `internal/service` | `shf service install\|uninstall\|status`: systemd user jedinica, launchd agent; pamti putanje `rsync`-a i `ssh`-a i postavlja `PATH`, odbija star rsync | 8.3 |
| `internal/update` | `shf update` i automatsko ažuriranje u `shf daemon`-u: paket verzije servera (`/healthz`) za `GOOS/GOARCH` sa `/v1/downloads`, provera prema `SHA256SUMS`, raspakivanje pored binarnog fajla, `version --short`, `os.Rename` | 8.5 |
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
shfd member list <share-id> [--json]       # otisak ključa člana i uređaji naloga sa svojim ključevima
shfd user add bob [--display-name "Bob B."]   # nalog sa lozinkom; ispisuje privremenu lozinku (samo tada)
shfd user passwd|disable|enable|show bob      # disable odmah odjavljuje sve uređaje naloga i briše njihove SSH ključeve
shfd user list
shfd member add <share-id> bob --user [--admin]   # nalog postaje član foldera (bez tokena)
shfd member link <share-id> marko marko           # postojeći član dobija nalog; token mu i dalje radi
shfd share list
shfd keys sync                             # blok u authorized_keys iz baza (uz --authorized-keys)
shfd serve --listen :8443 [--tls-cert cert.pem --tls-key key.pem] [--ssh-endpoint shfd@host:22]
           [--downloads /var/lib/shfd/downloads]   # paketi iz make release na /downloads/ (SHFD_DOWNLOADS)
           [--trusted-proxy 127.0.0.1,10.0.0.1]     # čiji X-Forwarded-For se veruje (SHFD_TRUSTED_PROXIES)

shfd trash list <share-id> [--path P] [--by U] [--reason deleted|replaced] [--since 2026-11-01]
shfd trash restore <share-id> <trash-id> [--to <putanja>]
shfd trash purge <share-id> (--older-than 90d | --id N | --all) [--dry-run] [--yes]
shfd verify <share-id> [--repair] [--json]   # izlazni kod 1 ako nađe neslaganje
shfd stats <share-id> [--json]
shfd db move --to /var/lib/shfd/db           # baze na novo mesto, dok server ne radi
shfd thumbs [--workers N] [--once] [--retry-failed]   # sličice (servis; treba mu shfd serve)
```

**Sličice.** `shfd thumbs` je poseban dugotrajan servis
(`shfd-thumbs.service`) koji pravi sličice slika (`jpg`, `png`, `gif`,
`webp`, `bmp`, `tiff`) i prve strane PDF-a (`pdftoppm` iz `poppler-utils`)
u tri veličine, u `<data-root>/<share-id>/thumbnails/`, po hash-u sadržaja.
SQLite ne otvara: poslove dobija od `shfd serve` preko `admin.sock`. GUI ih
dobija kroz `GET /v1/shares/{id}/admin/thumbs/{hash}?size=s|m|l`, a stanje
kroz polje `thumb` u `admin/files`
([docs/admin.md](docs/admin.md#thumbnails),
[docs/server-ssh.md](docs/server-ssh.md#7-sličice-shfd-thumbs)).

**Baze na lokalnom disku.** `storage/` (i `tmp/`, `trash/`, `service.log`)
može biti na NFS-u, ali SQLite baze (`registry.db`, `<share-id>/service.db`)
neka budu na lokalnom disku: `--db-root` / `SHFD_DB_ROOT`. SQLite ne
preporučuje WAL i POSIX lock-ove na mrežnom fajl-sistemu, a zaglavljen NFS
zaustavlja pisca foldera. `shfd serve` upozorava (`level=WARN`) kad su baze
na mrežnom disku; postojeće baze premešta `shfd db move`
([docs/server-ssh.md](docs/server-ssh.md#baze-na-lokalnom-disku),
[docs/admin.md](docs/admin.md#premeštanje-baza-shfd-db-move)).

**Nalozi.** Pored tokena člana (jedan token = jedan folder) server ima
naloge sa lozinkom: `POST /v1/auth/login` daje **device token** (`shd_…`)
kojim nalog vidi svoje foldere (`GET /v1/me`) i radi na svakom folderu u
kom je član, kao taj član. Naloge pravi samo admin servera (`shfd user
add`); privremena lozinka se menja pri prvoj prijavi. Lozinke su Argon2id,
pokušaji prijave su ograničeni po nalogu i po adresi (iza proxy-ja:
`--trusted-proxy`), GUI sesija ističe posle 12 h neaktivnosti (najviše 7
dana). Svaki uređaj naloga dobija za folder svoj token i svoj SSH ključ
(`POST /v1/shares/{id}/devices`, red `rsync-gate --device` u
`authorized_keys`), pa se uređaji istog člana ne otimaju, a opoziv jednog ne
dira ostale. Prijava tokenom radi kao i do sada. Opis, CLI i API:
[docs/admin.md](docs/admin.md#nalozi); proxy:
[docs/server-ssh.md](docs/server-ssh.md#adresa-klijenta-x-forwarded-for).

Username ostaje zauzet i kad je član opozvan: opozvanog člana vraća
`shfd member add` sa istim imenom (isti id i istorija, nov token, uloga prema
`--admin`, bez SSH ključa dok ga klijent ne registruje ponovo kroz `shf
join`). Za ime aktivnog člana `member add` vraća `member_exists`.

Admin komande su bezbedne dok `shfd serve` radi: idu kroz njegov lokalni
socket `<db-root>/admin.sock` i istog pisca foldera kao i klijenti. Opis i
primeri: [docs/admin.md](docs/admin.md).

Web GUI (prijava admin tokenom, članovi i tokeni, logovi uživo, fajlovi i
trash, uputstvo za instalaciju) je poseban servis **shfd-gui**, na korenu
hosta (`https://<host>/`, `SHFD_GUI_BASE=`, nginx po
`gui/deploy/nginx-gui-root.conf`) ili pod `https://<host>/gui`
(`gui/deploy/nginx-gui.conf`); isti build za oba: [docs/gui.md](docs/gui.md),
build `make gui`.

`GET /healthz` (i `GET /`, osim kad je shfd-gui na korenu hosta) bez tokena vraća `{"service":"shfd","version":…,"ok":true}`, a 503 kad registry baza ne odgovara ili kad pisac nekog otvorenog foldera ne odgovori u roku od 2 s (`"error":"share <id> stuck"`).

Bez `--tls-cert`/`--tls-key` server govori običan HTTP i očekuje TLS na
reverse proxy-ju. Kodovi grešaka API-ja su u `internal/server/errors.go`.
Za prenos fajlova: sistemski korisnik, `sshd` i `SHFD_AUTHORIZED_KEYS` su
opisani u [docs/server-ssh.md](docs/server-ssh.md).

Nova verzija na server (posle prvog, ručnog postavljanja) ide jednom komandom
sa mašine za build: `make deploy TARGET=<ime>` (`deploy/deploy.sh`) build-uje
`shfd`, `shfd-gui` i pakete iz `git archive` jednog commita, postavlja ih u
`versions/<sha>`, menja `current`, restartuje servise, proverava `/healthz` i
sama vraća prethodne verzije ako provera ne prođe:
[docs/deploy.md](docs/deploy.md).

## Klijent

Instalacija `shf` (gotov paket ili build): [docs/install.md](docs/install.md);
posle toga se `shf` ažurira sam, sa svog servera (`shf update`, vidi
[Ažuriranje](#ažuriranje-shf-update)). Na macOS-u posle **prve** instalacije
preuzetog paketa obavezno skinuti karantin, inače ga Gatekeeper ne pušta:

```sh
sudo xattr -d com.apple.quarantine "$(command -v shf)"
xattr -l "$(command -v shf)"        # ne sme da ispiše com.apple.quarantine
```

```sh
shf join https://shf.example.org <share-id> <token> ~/Tim   # pridruživanje i prvi sync
shf sync   [<folder>]    # jedan ciklus; izlazni kod 1 ako nešto nije uspelo
shf status [<folder>]    # poslednji sync, seq i HEAD, promene koje čekaju, problemi
shf log    [<folder>]    # poslednji ciklusi i šta je u njima urađeno
shf update [--check]     # verzija koju server koristi, i njena instalacija
shf version              # verzija ovog shf-a, servera, i da li ima ažuriranja
```

`join` u folder koji već ima fajlove samo spaja (ništa se ne briše). Stanje
foldera je van njega, u `$XDG_STATE_HOME/shf/<share-id>/` (na macOS-u
`~/Library/Application Support/shf/`); `SHF_STATE_HOME` menja koren. Za
prenos su potrebni `ssh` i rsync 3.2.4+; adresu sshd-a klijent dobija od
servera (`shfd serve --ssh-endpoint`) ili iz `--ssh`.

`shf` traži `rsync` i `ssh` u `PATH`-u; `SHF_RSYNC` i `SHF_SSH` (apsolutne
putanje) imaju prednost. Na macOS-u je `/usr/bin/rsync` Apple-ov `openrsync`
(predstavlja se kao 2.6.9) i ne radi: `brew install rsync`, pa Homebrew u
`PATH` ispred `/usr/bin`, npr. `eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"`
u `~/.zprofile` (radi i na Apple čipu, `/opt/homebrew`, i na Intel Mac-u, `/usr/local`). `rsync --version` u
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
`SHF_STATE_HOME`, upisuje se u jedinicu. Posle ručne zamene binarnog fajla
ponovo pokrenite `shf service install` (`shf update` to radi sam).

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
shf service install           # na kraju: rsync: /opt/homebrew/bin/rsync (Intel: /usr/local/bin/rsync)
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
  "bwlimit": 0,
  "auto_update": true,
  "update_interval": "24h"
}
```

`auto_update` uključuje automatsko ažuriranje u `shf daemon`-u (pri
pokretanju i zatim na `update_interval`); `false` ga isključuje, a
`shf update` i dalje radi ručno. Vidi [Ažuriranje](#ažuriranje-shf-update).

`bwlimit` ograničava brzinu svakog prenosa u KiB/s (rsync `--bwlimit`), 0 je
bez ograničenja. Prekinut prenos (pukla veza, ugašen ili srušen klijent) se
nastavlja od onoga što je već stiglo: server za istu najavu vraća isti
`upload_id`, a preuzimanje ostaje u `.shf-tmp-part-<hash>` pored odredišta.
Sa `shf daemon -v` svaki prenos se loguje (`msg=transfer … sent=… matched=…`).

### Ažuriranje (`shf update`)

**Klijent prati svoj server.** Verzije su kratki git hash-evi (`git
describe`) i ne mogu da se porede po veličini, pa `shf` ne traži
„najnoviju“ verziju: traži paket čija je verzija baš ona koju server
prijavljuje na `GET /healthz`, za svoj OS i arhitekturu, među paketima koje
server nudi (`shfd serve --downloads`, `GET /v1/downloads`).

```sh
shf update --check              # update available: shf 4ac5783 → 8f3da5e (…), ili zašto nema šta
shf update                      # pita, pa instalira; --yes bez pitanja
shf update --server https://…   # kad folderi na više servera imaju različite verzije
```

- Već je na verziji servera, server nema paket za ovaj sistem, ili server
  nije pokrenut sa `--downloads`: ne radi ništa i kaže to.
- Server se bira iz pridruženih foldera. Ako su folderi na više servera, svi
  moraju da odgovore i da imaju istu verziju; inače `shf update` ispiše
  verziju svakog i traži `--server <url>`, a automatsko ažuriranje se ne
  radi. Bez pridruženih foldera treba `--server`.
- Paket se preuzima bez tokena, sa rokom (10 min) i ograničenjem veličine
  (256 MiB), proverava se SHA-256 prema `SHA256SUMS` sa istog servera
  (paket koji tamo nije naveden ili se ne slaže se ne instalira), `shf` se
  raspakuje u privremeni fajl (`.shf-update-*`) **u istom direktorijumu** kao
  trenutni binarni fajl, nova verzija mora da prijavi verziju servera
  (`shf version --short`), i tek onda se jednim `rename`-om stavlja preko
  starog. Kad bilo šta od toga ne uspe, stari binarni fajl ostaje kakav je.
- Ako direktorijum binarnog fajla nije upisiv (npr. `/usr/local/bin`
  vlasništvo root-a), `shf update` paket ipak preuzme i proveri u privremeni
  direktorijum, pa ispiše komandu kojom se postavlja
  (`sudo install -m 0755 /tmp/shf-update-…/shf /usr/local/bin/shf`) i posle
  nje `shf service install`. Automatsko ažuriranje tada samo upiše u log da
  ne može. Ko želi da se `shf` ažurira sam, drži ga u direktorijumu u koji
  korisnik sme da piše (npr. `~/.local/bin`); vlasništvo nad samim fajlom
  nije dovoljno, jer se pravi nov fajl pored njega.
- Posle zamene, ako je servis instaliran, nova verzija sama ponovo upiše
  jedinicu ili plist (`shf service install`, sa putanjama `rsync`-a i `ssh`-a
  nađenim kao pri instalaciji) i restartuje ga. Bez servisa, `shf daemon`
  pokrenut ručno radi staru verziju dok se ponovo ne pokrene.

**Automatski, u `shf daemon`-u** (`auto_update`, podrazumevano uključeno):
pri pokretanju i zatim na `update_interval` (24 h) daemon radi isto što i
`shf update --yes`. Paket preuzima i proverava dok folderi normalno rade,
a zamenu radi **između ciklusa**: novi ciklus ne počinje, tekući (i prenos
u njemu) se završi, pa tek onda `rename`. Zatim zaustavi foldere i
**pokrene novi binarni fajl na mestu starog procesa** (`execve`, isti PID).
Zašto re-exec, a ne izlaz i `Restart=`/`KeepAlive`: radi isto pod systemd-om,
launchd-om i kad je `shf daemon` pokrenut ručno; jedinica ima
`Restart=on-failure`, pa uredan izlaz ne bi restartovao, a namerna greška bi
se brojala u ograničenja restartova (i `RestartSec`/`ThrottleInterval`
od 10 s); PID ostaje isti, pa systemd i launchd i dalje prate isti proces.
Definiciju servisa daemon upiše novom verzijom
(`shf service install --no-restart`: bez restarta, jer se restartuje sam),
pa važi od sledećeg pokretanja. Svaki pokušaj je jedan red u logu:

```
level=INFO msg="auto-update: up to date: shf 8f3da5e, server https://… runs 8f3da5e"
level=INFO msg="auto-update: installed shf 8f3da5e (was 4ac5783); restarting" from=https://…
level=WARN msg="auto-update failed; next try in 24h0m0s" err="…"
```

Posle neuspeha čeka sledeći interval (nema ponavljanja u petlji).

`shf version` pokazuje verziju ovog `shf`-a i, za svaki server pridruženih
foldera, njegovu verziju i da li je ažuriranje dostupno; `shf status` to
pokazuje za server foldera (red `version:`). `shf version --short` ispisuje
samo prvi red.

**macOS.** Fajl koji `shf` sam preuzme (HTTP iz Go-a) nema atribut
`com.apple.quarantine`, pa posle `shf update` nema Gatekeeper prozora i ne
treba `xattr`. Go linker binarne fajlove za `darwin/arm64` potpisuje ad-hoc
potpisom (u samom fajlu); `shf update` stavlja tačno bajtove iz paketa
(proverene SHA-256), u **nov fajl** koji `rename` stavi na mesto starog, pa
potpis ostaje važeći i macOS ne kešira potpis starog fajla (upis preko
postojećeg fajla, npr. `cp` na njega, je ono što na Apple čipu daje
`killed`).

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
deploy/          deploy.sh: nova verzija na server (docs/deploy.md)
```

## Instalacija

Gotovi paketi za Linux x86-64, macOS Intel i macOS Apple Silicon
(`make release`; preuzimaju se sa strane Instalacija u GUI-ju ili sa
`https://<host>/downloads/` kad server radi sa `--downloads`), šta treba na uređaju (rsync 3.2.4+, ssh), korak sa
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
