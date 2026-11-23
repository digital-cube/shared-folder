# Admin komande `shfd`: trash, verify, stats

Alati za admina servera: rad sa trash-om, provera da se `storage/` slaže sa
bazom i pregled stanja foldera (specifikacija, odeljci 3, 4 i 8.5). Članove
dodaje i opoziva `shfd member` (vidi [README](../README.md#server)).

Svaka operacija postoji na dva mesta, sa istim ponašanjem:

- kao **komanda na serveru** (`shfd trash …`, `shfd verify`, `shfd stats`);
- kao **admin API poziv** (`/v1/shares/{id}/admin/…`), dozvoljen samo članu
  sa ulogom `admin`. Član bez te uloge dobija `403 forbidden`, opozvan ili
  nepoznat token `401`.

```sh
export SHFD_DATA_ROOT=/var/lib/shfd/data    # ili --data-root uz svaku komandu
```

## Kako komanda stiže do foldera

Server obrađuje izmene jednog foldera jednu po jednu (jedini pisac,
odeljak 6). Admin komande idu kroz tog istog pisca, pa su bezbedne dok
servis radi:

- Dok radi `shfd serve`, on sluša na lokalnom unix socket-u
  `<data-root>/admin.sock` (prava 0600) i komanda šalje zahtev tamo. Ako je
  putanja predugačka za unix socket (preko ~100 znakova), socket je u
  `$TMPDIR/shfd-<uid>/<hash data-root-a>.sock`, u direktorijumu sa pravima
  0700.
- Kad server ne radi, komanda uzima isti lock na data-root-u koji drži
  `shfd serve` (pa server ne može da se pokrene u međuvremenu) i radi isto u
  svom procesu.
- Ako server radi, a socket ne odgovara (npr. stariji `shfd`), komanda
  odbija da radi i kaže zašto.

Komanda radi u ime najstarijeg aktivnog admina foldera; `--as <username>`
bira drugog (mora biti admin). To ime ostaje u dnevniku: ko je vratio,
očistio ili popravio.

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
