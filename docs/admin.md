# Admin komande `shfd`: članovi, trash, verify, stats, thumbnails

Alati za admina servera: članovi foldera (uloga, novi token, vraćanje
opozvanog), rad sa trash-om, provera da se `storage/` slaže sa bazom i
pregled stanja foldera (specifikacija, odeljci 3, 4 i 8.5). Za članove
postoji samo komanda `shfd member`, bez admin API poziva (vidi
[Članovi](#članovi)).

Svaka operacija postoji na dva mesta, sa istim ponašanjem:

- kao **komanda na serveru** (`shfd trash …`, `shfd verify`, `shfd stats`);
- kao **admin API poziv** (`/v1/shares/{id}/admin/…`), dozvoljen samo članu
  sa ulogom `admin`. Član bez te uloge dobija `403 forbidden`, opozvan ili
  nepoznat token `401`.

```sh
export SHFD_DATA_ROOT=/var/lib/shfd/data    # ili --data-root uz svaku komandu
export SHFD_DB_ROOT=/var/lib/shfd/db        # ili --db-root; baze na lokalnom disku
```

## Kako komanda stiže do foldera

Server obrađuje izmene jednog foldera jednu po jednu (jedini pisac,
odeljak 6). Admin komande idu kroz tog istog pisca, pa su bezbedne dok
servis radi:

- Dok radi `shfd serve`, on sluša na lokalnom unix socket-u
  `<db-root>/admin.sock` (prava 0600; db-root je data-root ako `--db-root`
  nije zadat) i komanda šalje zahtev tamo. Ako je putanja predugačka za unix
  socket (preko ~100 znakova), socket je u
  `$TMPDIR/shfd-<uid>/<hash db-root-a>.sock`, u direktorijumu sa pravima
  0700.
- Kad server ne radi, komanda uzima isti lock (`<db-root>/serve.lock`) koji
  drži `shfd serve` (pa server ne može da se pokrene u međuvremenu) i radi
  isto u svom procesu.
- Ako server radi, a socket ne odgovara (npr. stariji `shfd`), komanda
  odbija da radi i kaže zašto.

Komanda radi u ime najstarijeg aktivnog admina foldera; `--as <username>`
bira drugog (mora biti admin). To ime ostaje u dnevniku: ko je vratio,
očistio ili popravio.

## Članovi

```sh
shfd member add <share-id> <username> [--admin]      # nov član, ili vraćanje opozvanog
shfd member revoke <share-id> <username>
shfd member set-role <share-id> <username> admin|member
shfd member reset-token <share-id> <username>
shfd member list <share-id>
```

Komande `shfd member` ne idu kroz `admin.sock`: upisuju direktno u
`service.db` foldera, u kratkoj SQLite transakciji, kao `member add` i
`revoke` od početka. Bezbedne su i dok `shfd serve` radi (baza je u WAL
režimu, upisi čekaju jedan drugog). Ono što server drži u memoriji ne
menjaju odmah: otvorene WebSocket veze člana kome je token opozvan ili
zamenjen server zatvara pri sledećoj proveri, u roku od 10 s (u istom
procesu odmah). API pozivi starim tokenom dobijaju `401` odmah.

Username je jedinstven u folderu, **i za opozvane članove**: opozvan član
ostaje u listi i u istoriji (`updated_by`, „by“ u trash-u, dnevnik).

`member revoke` odbija poslednjeg aktivnog admina foldera (`last_admin`), iz
istog razloga kao `set-role` ispod: folder ne sme ostati bez admina. Prvo
dodeli ulogu nekom drugom, pa onda opozovi.

Iste promene radi i admin API (odeljak [Admin API](#admin-api)) i web GUI
(`docs/gui.md`), istim funkcijama kao ove komande.

### Jedan član = jedan uređaj

Server čuva **jedan SSH ključ po članu** (`members.ssh_pubkey`). Svaki
`shf join` registruje ključ uređaja na kome je pokrenut i time zamenjuje
prethodni. Ako se isti član pridruži sa drugog uređaja (npr. Mac i Linux kao
`marko`), prvi uređaj od tada dobija `Permission denied (publickey)` i
`shf status` na njemu pokazuje `state: key refused`. Zato svaki uređaj i svaka
osoba treba svog člana i svoj token: `shfd member add <share-id> marko-linux`.

Kad zamena ipak desi (drugi ključ, ne isti):

- `shf join` na novom uređaju ispisuje upozorenje sa otiskom zamenjenog
  ključa;
- u `service.log` foldera ide red
  ```
  2026-11-24T10:15:02.3Z key-replaced member=marko old=SHA256:… new=SHA256:…
  ```
- `PUT /v1/shares/{id}/ssh-key` u odgovoru ima `"replaced": {"fingerprint": "SHA256:…"}`.

Ponovljen `join` sa istog uređaja (isti ključ, npr. posle prekida) nije
zamena i ne ostavlja ništa od toga. Zamena se ne odbija; uređaj koji je
izgubio ključ vraća se ponovnim `shf join`, čime opet zamenjuje ključ onog
drugog.

Koji je uređaj trenutno aktivan pokazuje kolona `SSH KEY` u
`shfd member list`: SHA256 otisak registrovanog ključa (`-` ako ga član još
nema). Isti otisak daje `ssh-keygen -lf ~/.local/state/shf/<share-id>/id_ed25519.pub`
na uređaju (na macOS-u `~/Library/Application Support/shf/<share-id>/`).

```
USERNAME  ROLE    STATUS  LAST SEEN             LAST PUSH             ACKED SEQ  SSH KEY
ana       admin   active  2026-11-24T10:14:00Z  2026-11-24T09:58:12Z  41         SHA256:3vQk…
marko     member  active  2026-11-24T10:15:02Z  -                     41         SHA256:Yp7c…
ivan      member  active  -                     -                     0          -
```

### Uloga: `set-role`

```
$ shfd member set-role 3f0c… marko admin
marko: role member -> admin
```

Menja ulogu aktivnog člana i ispisuje staru i novu. Odbija:

- opozvanog ili nepoznatog člana (`unknown_member`);
- da poslednjem aktivnom adminu foldera oduzme ulogu (`last_admin`). Trash,
  verify i purge rade u ime najstarijeg aktivnog admina, pa folder ne sme
  ostati bez njega. Prvo dodeli ulogu nekom drugom, pa onda oduzmi.

Nova uloga važi od sledećeg API poziva člana; klijent ne treba ništa da
radi.

### Nov token: `reset-token`

```
$ shfd member reset-token 3f0c… bob
member: bob
token:  shf_…

The token is shown only now; it is stored as a hash.
```

Za člana koji je izgubio token ili mu je token procureo. Aktivni član dobija
nov token (ispisuje se samo tada, kao kod `member add`), a stari odmah
prestaje da važi: API vraća `401`, a WebSocket veze sa starim tokenom se
zatvaraju kao kod opoziva. SSH ključ člana ostaje u `authorized_keys`, pa
rsync radi i dalje; klijentu treba samo nov token (npr. ponovo `shf join`
sa novim tokenom). Opozvanog člana komanda odbija (`unknown_member`): njega
vraća `member add`.

### Vraćanje opozvanog člana: `member add` sa istim imenom

```
$ shfd member revoke 3f0c… marko
$ shfd member add 3f0c… marko --admin
member: marko (reactivated)
token:  shf_…
```

`member add` nad imenom opozvanog člana ne pravi novog člana nego vraća
starog:

- isti `id`, pa istorija (`updated_by`, „by“ u trash-u, dnevnik) ostaje
  njegova;
- nov token; stari ostaje nevažeći;
- uloga prema `--admin` (bez njega `member`), pa je `revoke` + `add
  --admin` takođe način da se promeni uloga;
- bez SSH ključa: stari ključ se ne vraća u `authorized_keys` (ni kroz
  `shfd keys sync`). Klijent ga registruje ponovo kroz `shf join` sa novim
  tokenom;
- `last_seq_acked` se vraća na 0, kao kod novog člana: klijent koji se
  ponovo pridruži kreće od početka dnevnika, a `stats` iz te vrednosti
  računa koliko član kasni (`behind`). `last_seen_at` i `last_push_at`
  ostaju (to je istorija) i menjaju se pri sledećem pozivu, odnosno upisu.

Nad imenom **aktivnog** člana `member add` i dalje odbija sa `member_exists`.

## Trash

Server čuva svaku obrisanu i pregaženu verziju u `<share-id>/trash/`, a
originalnu putanju pamti u tabeli `trash`. Trash se ne prazni sam.

### Lista

```
shfd trash list <share-id> [--path <prefiks>] [--by <username>]
                [--reason deleted|replaced] [--since <datum>]
```

- `--path` — originalna putanja počinje ovim tekstom (`docs/`, `docs/izv`).
- `--by` — član čija je operacija poslala verziju u trash.
- `--reason` — `deleted` (obrisan) ili `replaced` (pregažen novom verzijom).
- `--since` — od tog trenutka naovamo: `2026-11-20` (ponoć UTC) ili RFC 3339.

```
$ shfd trash list 493b54d3-e884-47ef-b8b1-af062f1a3adb
ID  PATH        REASON    BY   AT                    SIZE
1   docs/a.txt  deleted   ana  2026-11-23T18:01:28Z  4
2   b.txt       replaced  bob  2026-11-23T18:01:28Z  3
2 entries, 7 bytes
```

Isti fajl obrisan više puta daje više unosa (u `trash/` dobijaju `-2`, `-3`),
i svaki se može vratiti.

### Vraćanje

```
shfd trash restore <share-id> <trash-id> [--to <putanja>]
```

- Fajl se vraća na originalnu putanju, ili na `--to`.
- Roditeljski folderi koji više ne postoje se prave (`mkdir` događaji).
- Ako na toj putanji sada postoji **drugačiji** sadržaj (ili folder),
  postojeći se ne dira, a vraćeni fajl se upisuje kao
  `ime.restored-{ts}.ext` pored njega (`pathx.RestoredPath`; ako je i to ime
  zauzeto, `-2`, `-3` posle vremena).
- Ako putanja već ima **isti** sadržaj, ništa se ne menja.
- Unos ostaje u trash-u (vraćanje je kopija) i može se vratiti ponovo.
- U dnevnik ide događaj `restore` (putanja, hash, veličina, originalni mtime,
  `trash_id`; `old_path` je originalna putanja kad je fajl vraćen negde
  drugde). Klijenti ga dobijaju kao svaku drugu promenu, u običnom ciklusu.
- Ako fajl unosa nedostaje u `trash/` ili ima drugi sadržaj, vraćanje se
  odbija sa `trash_damaged`; tada pomaže `shfd verify`.

```
$ shfd trash restore 493b54d3-… 2
restored trash entry 2 as b.restored-20261123T180128Z.txt (seq 6): the original path holds other content and was left alone
$ shfd trash restore 493b54d3-… 1 --to arhiva/a.txt
restored trash entry 1 to arhiva/a.txt (seq 8)
```

### Čišćenje

```
shfd trash purge <share-id> (--older-than <starost> | --id <trash-id> | --all)
                 [--dry-run] [--yes]
```

Ovo je **jedina operacija u sistemu koja nepovratno uklanja sadržaj**.

- `--older-than` — unosi stariji od `36h`, `30d`, `2w` (ili bilo koje Go
  trajanje).
- `--id` — tačno ti unosi; može se ponoviti (`--id 3 --id 7`) ili `--id 3,7`.
- `--all` — sve.
- `--dry-run` — samo ispiše šta bi bilo obrisano; ništa ne menja.
- Bez `--dry-run` komanda prvo pokaže spisak i pita `[y/N]`; briše tačno
  ono što je pokazala, čak i ako je u međuvremenu stiglo nešto novo. `--yes`
  preskače pitanje (za skripte).

Za svaki obrisan unos u dnevnik ide događaj `purge` sa originalnom putanjom,
hash-om, veličinom, `trash_id` i imenom admina koji ga je pokrenuo. Red u
tabeli `trash` ostaje, označen kao očišćen (`purged_at`, `purged_by`,
`purge_seq`), jer se na njega pozivaju stariji događaji; lista, vraćanje i
verify ga više ne vide. Klijenti `purge` događaje preskaču, jer ne menjaju
`storage/`.

```
$ shfd trash purge 493b54d3-… --older-than 30d --dry-run
ID  PATH        REASON    BY   AT                    SIZE
1   docs/a.txt  deleted   ana  2026-10-02T09:12:40Z  4
would purge 1 entry, 4 bytes (dry run, nothing changed)
$ shfd trash purge 493b54d3-… --id 1
ID  PATH        REASON   BY   AT                    SIZE
1   docs/a.txt  deleted  ana  2026-10-02T09:12:40Z  4
Purge 1 entry, 4 bytes, for good? This cannot be undone. [y/N] y
purged 1 entry, 4 bytes
```

## `shfd verify <share-id> [--repair] [--json]`

Poredi `storage/` i `trash/` sa tabelama `files` i `trash` (računa hash
svakog fajla) i gleda ostatke u `tmp/`. Dok radi, drži pisca foldera, pa
uploadi tog foldera čekaju.

| Vrsta (`kind`) | Šta znači | `--repair` |
|---|---|---|
| `untracked` | fajl ili folder na disku kog nema u bazi | `put` (folder: `mkdir`) na ime admina |
| `missing` | red u bazi bez fajla (foldera) na disku | `delete` (`rmdir`) bez unosa u trash, uz upozorenje da je sadržaj izgubljen |
| `mismatch` | hash ili veličina se ne slažu sa bazom | `put` kao nova verzija; stara se ne može sačuvati, piše u izveštaju |
| `stale_tmp` | ostatak u `tmp/` stariji od 24 h koji ne pripada otvorenom uploadu | briše se |

Svaka popravka u `storage/` ide kroz dnevnik, pa je klijenti vide; u
`service.log` ide i red `verify-repair` po popravci.

U `trash/` (oblast `trash`): fajl bez reda dobija red (originalno ime,
razlog, član i vreme se čitaju iz imena kad ga je napravio server; putanja
foldera se ne zna), red bez fajla se beleži kao očišćen (`purge` događaj, uz
upozorenje), a red sa drugim hash-om preuzima hash i veličinu sa diska.

Ne popravlja se, samo prijavljuje: ime koje folder ne sme da sadrži (npr.
`CON`, nedozvoljen znak, ne-NFC oblik), ime koje se od postojećeg razlikuje
samo po veličini slova, symlink i drugi specijalni fajlovi.

Izlazni kod: bez `--repair` 1 ako je išta nađeno, inače 0; sa `--repair` 1
ako je nešto ostalo nepopravljeno. `--json` ispisuje ceo izveštaj
(`server.VerifyResult`).

```
$ shfd verify 493b54d3-…
checked 3 files, 2 directories, 1 trash entry, 0 tmp entries
AREA     KIND       PATH       DETAIL
storage  missing    b.txt      file in the database, not on disk
storage  untracked  rucno.txt  file 2d7116…4881 (1 bytes) on disk, not in the database
2 issues
shfd: 2 issues found; shfd verify --repair fixes them
$ shfd verify 493b54d3-… --repair
checked 3 files, 2 directories, 1 trash entry, 0 tmp entries
AREA     KIND       PATH       DETAIL                                   REPAIR
storage  missing    b.txt      file in the database, not on disk        repaired (seq 10)
storage  untracked  rucno.txt  file 2d7116…4881 (1 bytes) on disk, …    repaired (seq 11)
warning: storage b.txt: content 65f653…d87f (3 bytes) is lost; no trash entry
2 issues, 2 repaired
```

## `shfd stats <share-id> [--json]`

Broj i veličina fajlova u `storage/` (po bazi; `verify` proverava disk),
broj foldera, unosi u trash-u (i koliko je očišćeno), otvoreni uploadi,
HEAD, i po članu: uloga, poslednji put viđen (bilo koji uspešan API poziv),
poslednji put nešto poslao, do kog seq-a je potvrdio sinhronizaciju, koliko
kasni za HEAD-om i da li je opozvan. Admin operacije (vraćanje, čišćenje,
popravke) ne pomeraju „poslednji put poslao“.

```
$ shfd stats 493b54d3-…
share-id:  493b54d3-e884-47ef-b8b1-af062f1a3adb
head:      11
storage:   3 files, 8 bytes; 2 directories
trash:     1 entry, 3 bytes; 1 purged
uploads:   0 open

USERNAME  ROLE    STATUS  LAST SEEN             LAST PUSH             ACKED SEQ  BEHIND
ana       admin   active  2026-11-23T18:20:02Z  2026-11-23T18:01:28Z  11         0
bob       member  active  2026-11-23T18:19:55Z  2026-11-23T18:01:28Z  8          3
```

`--json` daje iste podatke (`server.Stats`):

```json
{
  "share_id": "493b54d3-e884-47ef-b8b1-af062f1a3adb",
  "head": 11,
  "files": 3, "files_size": 8, "dirs": 2,
  "trash": 1, "trash_size": 3, "purged": 1,
  "open_uploads": 0,
  "members": [
    {"username": "bob", "role": "member",
     "last_seen_at": "2026-11-23T18:19:55Z", "last_push_at": "2026-11-23T18:01:28Z",
     "last_seq_acked": 8, "behind": 3, "revoked": false, "revoked_at": null}
  ]
}
```

## Premeštanje baza: `shfd db move`

Baze (`registry.db` i `service.db` svakog foldera) treba da budu na lokalnom
disku, i kad je `storage/` na NFS-u ([server-ssh.md, Baze na lokalnom
disku](server-ssh.md#baze-na-lokalnom-disku)). Postojeće baze se premeštaju
ovako, **dok server ne radi**:

```sh
systemctl stop shfd
sudo -u shfd env SHFD_DATA_ROOT=/disk2/shared-storage \
    shfd db move --to /var/lib/shfd/db
```

`db move`:

1. uzima `serve.lock` trenutnog db-root-a (i novog); ako `shfd serve` radi,
   odbija sa „stop shfd serve before moving its databases“;
2. kopira `registry.db` i `<share-id>/service.db` svakog foldera iz registra
   u `<to>/registry.db` i `<to>/<share-id>/service.db` preko **SQLite backup
   API-ja**, ne `cp`-om, pa je i ono što je još u WAL-u (`-wal`) deo kopije;
3. na svakoj kopiji proverava `PRAGMA integrity_check` (mora biti `ok`) i da
   je verzija šeme (`user_version`) ista kao kod originala;
4. ako u `<to>` već postoji neka od tih baza, ne kopira ništa;
5. **stare baze ostavlja** gde su; obrišite ih (`*.db`, `*.db-wal`,
   `*.db-shm`) tek kad server radi kako treba na novim;
6. u data-root upisuje fajl `db-moved` sa novim db-root-om. Svaka `shfd`
   komanda pokrenuta nad tim data-root-om sa drugim db-root-om (npr. bez
   `SHFD_DB_ROOT`) odbija da radi, umesto da tiho koristi stare kopije;
7. ispisuje šta treba promeniti.

Posle toga, pre pokretanja servera:

```ini
# /etc/systemd/system/shfd.service, [Service]
Environment=SHFD_DB_ROOT=/var/lib/shfd/db
```

```sh
systemctl daemon-reload
# sa rsync-om preko SSH-a: prinudne komande u authorized_keys dobijaju --db-root
sudo -u shfd env SHFD_DATA_ROOT=/disk2/shared-storage SHFD_DB_ROOT=/var/lib/shfd/db \
    SHFD_AUTHORIZED_KEYS=/var/lib/shfd/.ssh/authorized_keys shfd keys sync
systemctl start shfd
curl -s http://127.0.0.1:8443/healthz
```

Ponovno premeštanje (npr. na drugi lokalni disk) ide isto, sa trenutnim
`--db-root`. Vraćanje u data-root (`--to <data-root>`) briše `db-moved`, ali
tek pošto se tamošnje stare kopije uklone.

## Admin API

Autorizacija kao i ostatak API-ja (`Authorization: Bearer <token>`), ali
samo za člana sa ulogom `admin`. Greške su
`{"error": {"code": "...", "message": "..."}}`.

| Poziv | Telo / parametri | Odgovor |
|---|---|---|
| `GET /v1/shares/{id}/admin/trash` | `?path=&by=&reason=&since=` | `{entries, count, total_size}` |
| `POST /v1/shares/{id}/admin/trash/{trash_id}/restore` | `{"to": "putanja"}` (opciono) | `{trash_id, status, path, renamed, events, head}` |
| `POST /v1/shares/{id}/admin/trash/purge` | `{"older_than": "30d"}` ili `{"ids": [1,2]}` ili `{"all": true}`, uz `"dry_run": true` | `{dry_run, entries, count, total_size, events, head}` |
| `POST /v1/shares/{id}/admin/verify` | `{"repair": false}` | `{repair, issues, checked, events, head}` |
| `GET /v1/shares/{id}/admin/stats` | — | `server.Stats` |
| `GET /v1/shares/{id}/admin/members` | — | `{head, members: [{username, role, status, created_at, revoked_at, last_seen_at, last_push_at, last_seq_acked, behind, ssh_fingerprint, connected}]}` |
| `POST /v1/shares/{id}/admin/members` | `{"username": "bob", "admin": false}` | `{member, token, reactivated?}` |
| `POST /v1/shares/{id}/admin/members/{username}/revoke` | — | `{member}` |
| `POST /v1/shares/{id}/admin/members/{username}/role` | `{"role": "admin"}` ili `"member"` | `{member, old_role}` |
| `POST /v1/shares/{id}/admin/members/{username}/reset-token` | — | `{member, token}` |
| `GET /v1/shares/{id}/admin/log` | `?after=<bajt-offset>&limit=<n>` | `{offset, next_offset, size, lines}` |
| `GET /v1/shares/{id}/admin/files` | `?dir=<putanja>` (prazno = koren) | `{dir, entries}` |
| `GET /v1/shares/{id}/admin/files/content` | `?path=<putanja>` (obavezno), `&download=1` | sadržaj fajla ([Sadržaj fajla](#sadržaj-fajla)); `Range`, `If-None-Match` |
| `GET /v1/shares/{id}/admin/thumbs/{hash}` | `?size=s\|m\|l` (podrazumevano `m`) | `image/jpeg` ([Thumbnails](#thumbnails)) |

Uz njih postoji i `GET /v1/whoami`, koji **ne traži** ulogu admin: za
`Authorization: Bearer shf_…` vraća `{share_id, share_name, username, role}`.
Token je vezan za jedan folder, pa je dovoljan za prijavu (tako radi
shfd-gui); nepoznat ili opozvan token dobija `401`.

**Članovi.** Pozivi menjaju članove istim funkcijama kao `shfd member …`, sa
istim posledicama: posle `revoke` i `reset-token` stari token odmah dobija
`401`, a WebSocket veze člana se zatvaraju. `status` je `active`, `pending`
(aktivan, ali još nema SSH ključ: `shf join` nije završen) ili `revoked`;
`ssh_fingerprint` je otisak jedinog ključa člana, `connected` broj njegovih
otvorenih WebSocket veza ka ovom procesu. **Token** stoji samo u odgovoru na
`add` i `reset-token`; čuva se samo njegov hash i ne loguje se nigde. Greške
kao u CLI-ju: `member_exists` (409), `unknown_member` (404), `last_admin`
(409: oduzimanje uloge ili opoziv poslednjeg admina), `invalid_request` (400:
loše ime ili uloga).

**Log.** `admin/log` čita `service.log` kao parsirane redove. Bez `after`
vraća poslednjih `limit` redova (podrazumevano 200, najviše 5000); sa
`after=<next_offset>` prethodnog odgovora vraća samo ono što je stiglo posle
(red koji se još upisuje čeka sledeći poziv). Red je
`{offset, ts, type, seq?, member, op, path, old_path?, dir?, outcome?, detail?, raw}`;
`type` je `event` (izmena u folderu, `op` je `put`, `delete`, `move`, …),
`transfer` (rsync-gate; `op` = `dir` = `upload`/`download`/`-`, `outcome`
`ok`/`denied`/`failed`), `key-replaced` (`detail` = `old=… new=…`), `sync`
(izveštaj klijenta posle ciklusa; `outcome` `ok`/`failed`, `detail` =
`from=… to=… sent=… received=… conflicts=… took=…` i `error`) ili
`other` (red koji nije parsiran, samo `raw`). `offset` je bajt-pozicija reda
u fajlu i služi kao njegov id.

**Fajlovi.** `admin/files` vraća jedan nivo iz tabele `files` (disk se ne
čita): `{path, name, kind, size, mtime, hash, seq, updated_by, thumb}`, prvo
folderi, pa po imenu. `thumb` je stanje sličice ([Thumbnails](#thumbnails)):
`ready`, `pending`, `failed` ili `""`. Nepostojeći folder je `not_found` (404), fajl umesto
foldera `kind_mismatch` (409).

<a id="sadržaj-fajla"></a>
**Sadržaj fajla.** `GET /v1/shares/{id}/admin/files/content?path=<putanja>`
šalje sadržaj jednog fajla, samo adminu foldera (i preko `admin.sock`, kao
ostale admin rute; ne osvežava `last_seen_at`). Pravila:

- Putanja prolazi `pathx.Normalize` (`..`, apsolutna putanja, kontrolni
  znaci → `invalid_path`, 400) i mora biti red tabele `files` sa
  `kind=file`: fajl koji postoji samo na disku je `not_found` (404), folder
  `kind_mismatch` (409). Fajl se otvara kroz `os.Root` nad `storage/`, pa
  symlink koji vodi van `storage/` ne radi.
- **Sadržaj mora imati hash iz baze.** Pre prvog bajta fajl se hešira
  (SHA-256) i poredi sa `files.hash`; ako fajla nema, ako mu je druga
  veličina ili drugi hash, odgovor je `409 file_changed` („pokreni shfd
  verify“) i ništa se ne šalje. Proveren fajl se pamti po inode-u, veličini
  i mtime-u (najviše 4096 putanja u memoriji), pa `Range` zahtevi istog
  preuzimanja ne heširaju ga ponovo; fajl u `storage/` se uvek menja
  rename-om, nikad na mestu, pa nova verzija ima drugi inode.
- Šalje se kroz `http.ServeContent`: `Accept-Ranges: bytes`, `Range` (206),
  `If-Range`, `If-None-Match` (304), `HEAD`. `ETag` je `"<hash>"`,
  `Cache-Control: private, no-cache` (browser pita ponovo, dobija 304).
- **Bezbednost.** Uvek `X-Content-Type-Options: nosniff` i
  `Content-Security-Policy: sandbox`. Tip je iz bele liste po ekstenziji:
  `jpg/jpeg/png/gif/webp` (slike), `pdf` i `txt/md/csv/log/json` (`md`,
  `csv` i `log` kao `text/plain; charset=utf-8`) idu sa
  `Content-Disposition: inline`; **sve ostalo**, i `html`, `htm`, `svg`,
  `xml`, `js`, ide kao `application/octet-stream` sa `attachment`.
  `download=1` uvek daje `attachment`. Ime je po RFC 6266/5987:
  `filename="…"` sa ASCII zamenom (ne-ASCII, `"`, `\`, `%` → `_`) i
  `filename*=UTF-8''…` sa tačnim imenom kad se razlikuju.

```sh
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" -OJ \
  "https://shf.example.com/v1/shares/$SHARE/admin/files/content?path=docs%2Fplan.pdf&download=1"
```

```sh
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" https://shf.example.com/v1/whoami
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" -d '{"username":"bob"}' \
  "https://shf.example.com/v1/shares/$SHARE/admin/members"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://shf.example.com/v1/shares/$SHARE/admin/log?after=10240"
```

Kodovi grešaka: `forbidden` (403) za člana bez uloge admin,
`unknown_trash` (404) za nepoznat ili očišćen unos, `trash_damaged` (409)
kad fajl unosa nedostaje ili ima drugi sadržaj, `invalid_request` (400) za
pogrešan filter ili izbor kod čišćenja.

```sh
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://shf.example.com/v1/shares/$SHARE/admin/trash?by=bob&reason=deleted"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" -d '{"older_than":"90d","dry_run":true}' \
  "https://shf.example.com/v1/shares/$SHARE/admin/trash/purge"
```

Isti pozivi, bez tokena, idu kroz `admin.sock`; zaglavlje `X-Shfd-As`
bira admina (to koristi `--as`).

## Thumbnails

Sličice slika i PDF-ova za GUI (specifikacija, odeljak 3 „Thumbnails“).
Pravi ih `shfd thumbs`, dugotrajan servis koji radi samo to, u posebnom
procesu (jedinica `shfd-thumbs.service`, [server-ssh.md](server-ssh.md#7-sličice-shfd-thumbs)).

```sh
shfd thumbs [--workers N] [--pdftoppm PATH] [--once] [--retry-failed]
            [--max-image-mib 64] [--max-megapixels 64] [--max-pdf-mib 256]
            [--pdf-timeout 30s]
```

- Treba mu pokrenut `shfd serve` na istom `--db-root`-u: poslove dobija
  preko `admin.sock`, a **SQLite ne otvara**. Fajlove čita iz `storage/`,
  piše samo u `<data-root>/<share-id>/thumbnails/`. Kad server ne radi,
  čeka i pokušava ponovo na 2 s (u logu jedan red kad server nestane i
  jedan kad se vrati). Drugi `shfd thumbs` na istom db-root-u odbija da
  radi (`<db-root>/thumbs.lock`).
- `--workers N` (podrazumevano 1): koliko sličica pravi odjednom.
- `--pdftoppm PATH` (ili `$SHFD_PDFTOPPM`, inače `pdftoppm` iz `PATH`-a):
  za PDF-ove (`apt install poppler-utils`). Bez njega servis jednom upiše
  WARN i ne uzima PDF poslove; slike rade normalno.
- `--once`: obradi sve što čeka, očisti i izađi; za prvo popunjavanje i
  testove. Bez servera izlazi sa kodom 1.
- `--retry-failed`: pre rada obriše sve `.fail` markere, pa se ti fajlovi
  pokušaju ponovo (npr. posle `--max-…` sa većom granicom ili posle
  instalacije `pdftoppm`-a).
- Granice su po fajlu: slika (veličina fajla i širina × visina iz
  zaglavlja, pre dekodiranja), PDF (veličina fajla) i rok za `pdftoppm`
  (posle njega se ubija cela grupa procesa).

Log ide kroz `slog`, kao kod `serve`: po poslu red sa folderom, hash-om,
vrstom, trajanjem i ishodom (`ready`, `failed` sa razlogom, `skipped`).
Putanje i sadržaj fajlova ne idu u log iznad nivoa Debug.

```
level=INFO msg=thumbnail share=45b2… hash=e0bc… kind=image took=41ms outcome=ready
level=INFO msg=thumbnail share=45b2… hash=9f1a… kind=pdf took=30.002s outcome=failed reason="pdftoppm timed out after 30s"
level=INFO msg="thumbnail cleanup" removed=3 took=12ms
```

**Markeri.** `thumbnails/<hh>/<hash>.fail` je tekst sa razlogom („image of
9000x9000 pixels is over the limit of 64000000“, „not a readable image: …“,
„decoder panic: …“, „pdftoppm timed out after 30s“, „crashed while
generating“). Hash sa `.fail` se ne pokušava ponovo sam od sebe; GUI za
njega prikazuje ikonicu tipa. `<hash>.work` stoji dok se sličica pravi;
ako ga servis zatekne pri startu (pao je baš na tom fajlu, npr. OOM), on
postaje `.fail` sa „crashed while generating“. Ručno: `rm` `.fail` fajla
ili `--retry-failed` za sve.

**Čišćenje.** Pri startu i jednom dnevno servis za svaki hash iz
`thumbnails/` pita server (u paketima do 1000) da li ga još drži neki fajl
ili nepurgovan unos u trash-u; ostale briše (`.jpg` i `.fail`). Posle
`shfd trash purge` sličice tih fajlova nestaju pri sledećem čišćenju.

**Stanje u `admin/files`** (polje `thumb`):

| `thumb` | Značenje |
|---|---|
| `ready` | sličica postoji |
| `pending` | kandidat je, sličice još nema, a servis koji pravi tu vrstu (slika ili PDF) tražio je poslove u poslednjih 60 s |
| `failed` | postoji `.fail` |
| `""` | folder, tip bez sličice, prazan fajl, ili servis ne radi |

**Slika:** `GET /v1/shares/{id}/admin/thumbs/{hash}?size=s|m|l`
(podrazumevano `m`), samo za admina foldera, vraća `200 image/jpeg` sa
`Cache-Control: private, max-age=31536000, immutable`,
`ETag: "<hash>.<size>"` (`304` na `If-None-Match`) i
`X-Content-Type-Options: nosniff`. `404 not_found` kad sličice nema;
`400 invalid_request` za hash koji nije `^[0-9a-f]{64}$` ili nepoznatu
veličinu (proverava se pre bilo kakvog pristupa disku). Čita se samo iz
`thumbnails/` tog foldera, pa admin drugog foldera ne može da dobije tuđu
sličicu. Poziv ne osvežava `last_seen_at` (folder slika traži stotine).

```sh
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" -o thumb.jpg \
  "https://shf.example.com/v1/shares/$SHARE/admin/thumbs/$HASH?size=l"
```

**Rute samo na `admin.sock`** (za `shfd thumbs`; na javnom API-ju `404`):

| Poziv | Telo / parametri | Odgovor |
|---|---|---|
| `GET /v1/thumbs/jobs` | `?limit=50&kinds=image,pdf[&rescan=1]` | `{"jobs":[{share_id, hash, path, size, kind}], "pending": N}` |
| `POST /v1/thumbs/done` | `{"share_id", "results":[{hash, ok, error, skipped}]}` | `{"status":"ok"}` |
| `POST /v1/shares/{id}/admin/thumbs/unreferenced` | `{"hashes":[…]}` (najviše 1000) | `{"hashes":[…]}`: oni koje ništa ne drži |

`jobs` vraća fajlove (veličina > 0, podržana ekstenzija, vrsta iz `kinds`)
čiji hash nema ni gotovu sličicu ni `.fail`, jedan posao po hash-u,
**najnoviji prvo** (po `seq`), naizmenično iz svih foldera; `pending` je
ukupan broj takvih hash-eva. Server pamti kad je servis poslednji put pitao
i za koje vrste (od toga zavisi `pending` u `admin/files`). `rescan=1`
(servis ga šalje pri startu) natera server da ponovo pročita `thumbnails/`.
`done` sa `skipped: true` znači da fajl nije bio ono što posao kaže
(promenjen, premešten ili obrisan u međuvremenu); taj hash se ne nudi
ponovo 5 minuta. Ako servis padne posle upisa sličice a pre `done`, posao
dođe ponovo, a servis vidi da sličica postoji i samo javi `done`.
