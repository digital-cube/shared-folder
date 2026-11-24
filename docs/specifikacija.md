# Shared folder servis — specifikacija prve verzije

Datum: 2026-11-23. Izvor: Planning chat i odgovori u tiketu TCKY3HE5MN.
Radna imena: server `shfd`, klijent `shf`, fajl za ignorisanje `.shfignore`.

## 1. Cilj

Deljeni folder za mali tim (do oko 10 članova po folderu). Kopija na serveru je
jedina zajednička istina; klijenti su iza NAT-a i nikad ne komuniciraju
direktno. Kad jedan član doda, izmeni ili obriše fajl, ostali to dobiju u roku
od nekoliko sekundi, bez ikakve akcije. Ništa što je ikad stiglo na server ne
nestaje bez odluke admina.

## 2. Odluke (TCKY3HE5MN)

| Tema | Odluka |
|---|---|
| Prenos fajlova | rsync preko SSH-a u prvoj verziji, iza interfejsa `Transport`, da se kasnije može dodati HTTPS |
| Platforme | Linux i macOS prvo, Windows odmah posle |
| Identitet | ključ foldera + token po članu, token se može opozvati |
| Uloge | admin i rw članovi; rw članovi sami prave foldere |
| Konflikt sadržaja | prvi zadržava ime, drugi postaje `ime.conflict-{user}-{timestamp}.ext` |
| Izmena protiv brisanja | izmena pobeđuje, stara verzija ide u trash |
| Pregažene verzije | idu u trash kao `.replaced-by-{user}-{timestamp}` |
| Trash | čuva se zauvek; čišćenje pokreće admin iz CLI-ja (kasnije UI ili automatika) |
| Masovno brisanje | zaštita da, ali desktop prozor za potvrdu ide u kasniju fazu (vidi 8.4) |
| Obaveštavanje | WebSocket signal + rezervni poll + povremeno puno poređenje |
| Klijent | pozadinski servis + CLI; tray/GUI kasnije |
| Obim synca | prva verzija skida ceo folder |
| Jezik | Go, jedan binarni fajl za server i jedan za klijent |
| Hosting | self-hosted, javna adresa, TLS, više foldera na jednom serveru |
| `storage/` na serveru | piše samo servis; postoji admin komanda za proveru i popravku |
| Symlinkovi, prava | ne sinhronizuju se |
| Enkripcija | TLS u prenosu, bez end-to-end u prvoj verziji |
| Veličine | tipično do 20 MB, ali veći fajlovi su mogući; prekinut prenos se nastavlja |

## 3. Raspored na serveru

```
<data-root>/
  registry.db                 spisak foldera (id, ime, kad je napravljen)
  <share-id>/                 share-id je UUID
    storage/                  trenutno stanje foldera, obični fajlovi
    trash/                    obrisane i pregažene verzije
    tmp/                      dolazni uploadi (isti fajl sistem kao storage, zbog atomskog rename-a)
    thumbnails/               sličice slika i PDF-ova (shfd thumbs), po hash-u sadržaja
    service.db                SQLite
    service.log               čitljiv ispis događaja
```

### service.db

- `members`: id, username (jedinstven u folderu, i za opozvane članove:
  opozvan član se vraća istim imenom, `shfd member add`, sa istim id-jem i
  istorijom, novim tokenom i bez SSH ključa), token_hash, ssh_pubkey, role
  (`admin` | `member`), created_at, revoked_at, last_seen_at, last_push_at,
  last_seq_acked.
- `files`: trenutno stanje — path, kind (`file` | `dir`), hash, size, mtime,
  seq (poslednja promena), updated_by.
- `events`: dnevnik promena — seq (rastući, bez rupa; najveći je HEAD), ts,
  member, op (`put` | `delete` | `move` | `mkdir` | `rmdir` | `conflict` |
  `restore` | `purge`), path, old_path, hash, prev_hash, trash_id.
- `trash`: id, orig_path, trash_name, hash, size, reason (`deleted` |
  `replaced`), by_member, at, event_seq.
- `sync_log`: member, started_at, finished_at, from_seq, to_seq, sent, received,
  conflicts, error.
- `member_devices` (migracija `0006`, TCKCQMRX95): kredencijali uređaja
  naloga za ovaj folder — member_id, device_id (`registry.db` `devices.id`),
  token_hash (jedinstven), ssh_pubkey, created_at, revoked_at;
  `UNIQUE (member_id, device_id)`. Identitet u istoriji ostaje član
  (`updated_by`), uređaj je samo kredencijal; `members.token_hash` i
  `members.ssh_pubkey` ostaju kao kredencijal člana (legacy)
  ([admin.md](admin.md#kredencijali-uređaja-za-folder)).

`events` je izvor istine; `service.log` je njegov čitljiv ispis, plus jedan red
po sinhronizaciji iz `sync_log`.

### Trash

Ime u trash-u: `<originalno ime>.deleted-by-{username}-{timestamp}` ili
`.replaced-by-{username}-{timestamp}`, timestamp u UTC (`20261123T145846Z`).
Trash je ravan direktorijum; originalna putanja je u tabeli `trash`, pa se zna
gde se fajl vraća. Ako dva unosa dobiju isto ime, dodaje se `-2`, `-3`.

Implementacija (TCKTHRC7PQ, [admin.md](admin.md)): vraćanje kopira fajl iz
trash-a (unos ostaje) i upisuje događaj `restore`; ako je putanja zauzeta
drugim sadržajem, fajl dobija ime `ime.restored-{ts}.ext`
(`pathx.RestoredPath`). Čišćenje briše fajl iz `trash/` i upisuje događaj
`purge` (originalna putanja, hash, veličina, ko); red u tabeli `trash` ostaje
označen sa `purged_at`, `purged_by`, `purge_seq` (migracija 0004), jer se na
njega pozivaju stariji događaji.

### Thumbnails

Sličice za GUI (TCKU55DNMU, [admin.md](admin.md#thumbnails)). Pravi ih
poseban proces istog binarnog fajla, `shfd thumbs` (systemd
`shfd-thumbs.service`), da dekodiranje tuđih fajlova (CPU, memorija, pad
dekodera) ne dira `shfd serve`, kroz koji ide sync. On **ne otvara SQLite**:
poslove dobija od živog `shfd serve` preko `admin.sock`, fajlove čita iz
`storage/`, a piše samo u `thumbnails/`.

```
thumbnails/<prva 2 znaka hash-a>/<hash>.l.jpg    512 px
                                 <hash>.m.jpg    256 px
                                 <hash>.s.jpg     96 px
                                 <hash>.fail     razlog neuspeha (tekst)
                                 <hash>.work     u izradi
```

- **Ključ je hash sadržaja** (`files.hash`), ne putanja: preimenovanje,
  premeštanje, kopija i vraćanje iz trash-a ne traže novu sličicu, nov
  sadržaj ima nov hash, a URL sličice se nikad ne menja (browser je kešira
  trajno). Direktorijum ostaje u data-root-u (ne u db-root-u) i pravi se pri
  otvaranju foldera, kao `storage/` (0700).
- **Veličine** (duža stranica, proporcije se čuvaju, manja slika se ne
  uvećava): `s` 96, `m` 256, `l` 512 px. JPEG, kvalitet 82, providnost na
  beloj pozadini. Upisuju se redom `l`, `m`, `s`, svaka atomski (privremeni
  fajl u istom direktorijumu pa `rename`); sličica je **gotova** kad postoji
  `<hash>.s.jpg`.
- **Šta dobija sličicu**, po ekstenziji imena (bez obzira na velika i mala
  slova): `jpg`, `jpeg`, `png`, `gif` (prvi kadar), `webp`, `bmp`, `tif`,
  `tiff` i `pdf` (prva strana). Fajl veličine 0 ne. Slike se dekodiraju u
  čistom Go-u (`image/*`, `golang.org/x/image`), uz EXIF orijentaciju JPEG-a;
  PDF ide kroz `pdftoppm` (poppler-utils), sa rokom od 30 s posle kog se
  ubija cela grupa procesa.
- **Granice:** slika do 64 MiB i 64 megapiksela (iz zaglavlja, pre
  dekodiranja), PDF do 256 MiB. Preko granice, oštećen fajl, panika
  dekodera ili istekao rok daju `<hash>.fail`; takav hash se ne pokušava
  ponovo dok ga admin ne pusti (`shfd thumbs --retry-failed`).
- Pre posla se upiše `<hash>.work`; ako proces padne na tom fajlu, pri
  sledećem startu `.work` postaje `.fail` („crashed while generating“).
- Dok čita izvor, servis računa SHA-256; ako se ne poklapa sa hash-om posla
  (fajl se u međuvremenu promenio), posao se preskače bez traga.
- `shfd serve` po folderu drži u memoriji skup gotovih i skup neuspelih
  hash-eva (jedan prolaz kroz `thumbnails/` pri prvoj upotrebi, posle samo
  izveštaji servisa), pa listanje foldera ne radi `stat` po sličici.
- Sličica koju više ne drži ni `files` ni nepurgovan red u `trash` briše se
  pri startu servisa i jednom dnevno. Sličice u trash-u se ne prikazuju, ali
  ostaju dok se unos ne očisti, pa vraćanje iz trash-a ne traži novu.
- `shfd verify` ne gleda `thumbnails/`; `rsync-gate` ne pušta ni čitanje ni
  upis u njega. Klijent `shf` sličice ne sinhronizuje.

## 4. Kontrolni API (HTTPS + JSON)

Autorizacija: `Authorization: Bearer <token člana>`. TLS preko reverse proxy-ja
ili ugrađen. Rute foldera primaju i folder-token uređaja naloga
(`POST /v1/shares/{id}/devices`) i device token naloga koji je aktivan član
foldera; sva tri rade u ime člana (TCK3C27P32, TCKCQMRX95,
[admin.md](admin.md#nalozi)).

- `GET  /v1/shares/{id}/changes?since=N` — događaji posle N i trenutni HEAD.
- `POST /v1/shares/{id}/uploads` — najava: path, base_hash (verzija koju je
  klijent imao, prazno za nov fajl), new_hash, size. Odgovor: upload_id i
  odredište za transport. Ako server već ima new_hash na toj putanji, odgovor
  je „ništa za slanje“.
- `POST /v1/shares/{id}/uploads/{upload_id}/commit` — server proverava hash
  pristiglog fajla, primenjuje pravila iz odeljka 6, atomski premešta u
  `storage/`, upisuje događaj i javlja ostalima.
- `POST /v1/shares/{id}/ops` — `delete`, `move`, `mkdir`, `rmdir`, svaki sa
  base_hash gde ima smisla.
- `GET  /v1/shares/{id}/ws` — WebSocket; server šalje samo `{"head": N}`.
- Admin: članovi (dodaj ili vrati opozvanog, opozovi, promeni ulogu, nov
  token), trash (lista, vrati, očisti), statistike,
  provera `storage/` prema bazi.
- Bez tokena, kad je `shfd serve` dobio `--downloads DIR`: `GET /v1/downloads`
  (paketi iz `make release` u `DIR`, najnoviji prvi, sa `sha256` iz
  `SHA256SUMS`) i `GET /downloads/<ime>` (paket ili `SHA256SUMS`)
  ([install.md](install.md#objavljivanje-paketa-na-serveru)).

Implementacija (TCKTHRC7PQ): `GET /admin/trash`, `POST /admin/trash/{id}/restore`,
`POST /admin/trash/purge`, `POST /admin/verify`, `GET /admin/stats` pod
`/v1/shares/{id}`, samo za ulogu `admin` (ostali dobijaju 403 `forbidden`).
Članovi se i dalje dodaju, opozivaju i menjaju (uloga, nov token, vraćanje
opozvanog) samo komandom `shfd member`, bez admin API poziva. `shfd serve`
iste pozive služi bez tokena na lokalnom unix socket-u `<data-root>/admin.sock`
(0600), kroz koji idu `shfd` admin komande dok server radi
([admin.md](admin.md)).

## 5. Prenos fajlova

Interfejs `Transport` sa dve operacije: pošalji fajl za dati upload_id i preuzmi
fajl po putanji i hash-u. Prva implementacija je rsync preko SSH-a:

- Jedan sistemski korisnik na serveru; svaki član ima svoj SSH ključ koji
  klijent napravi pri pridruživanju. U `authorized_keys` stoji sa prinudnom
  komandom (`command="shfd rsync-gate --share … --member …"`), kao kod
  gitolite-a. Opoziv člana briše taj red.

  Implementacija (TCKCQMRX95): uređaj naloga ima svoj ključ po folderu, sa
  svojim redom (`… --member M --device D`, komentar
  `shfd:<član>/<uređaj>@<share>`), pa se uređaji istog člana ne otimaju.
  `rsync-gate --device` pri svakom prenosu proverava da su član, red u
  `member_devices`, uređaj i nalog aktivni, inače odbija sa
  `denied (device_revoked)` i upisuje to u `service.log`. Opoziv uređaja (u
  folderu, na nalogu, `shfd user disable`) i opoziv člana brišu njegove redove
  ([server-ssh.md](server-ssh.md#5-šta-shfd-upisuje-u-authorized_keys)).
- `rsync-gate` dozvoljava samo rsync, i to: upis isključivo u
  `tmp/<upload_id>` i čitanje isključivo iz `storage/`. Klijent nikad ne piše
  direktno u `storage/`, tako da servis ostaje jedini pisac.
- Upload ide sa `--partial`, pa se prekinut prenos nastavlja.

  Implementacija (TCK292YM96): server za istu najavu istog člana (putanja,
  base_hash, new_hash, veličina) dok nije commit-ovana vraća isti `upload_id`,
  pa se upload nastavlja i kad se klijent u međuvremenu ugasi ili sruši.
  Preuzimanje ide u `.shf-tmp-part-<hash>` pored odredišta, koji ostaje kad se
  prenos prekine; sledeće preuzimanje istog sadržaja nastavlja od njega.
- Posle preuzimanja klijent proverava hash; ako je fajl u međuvremenu
  zamenjen, ponavlja preuzimanje.

Hash je SHA-256 celog fajla.

## 6. Pravila na serveru

Server obrađuje operacije jednu po jednu po folderu, pa „istovremeno“ uvek
znači „zasnovano na zastareloj verziji“.

1. **Isti sadržaj**: new_hash je jednak trenutnom — nema promene, nema događaja.
2. **Redovna izmena**: base_hash je jednak trenutnom — stara verzija ide u trash
   (`replaced`), nova zauzima ime.
3. **Konflikt**: base_hash se razlikuje od trenutnog — postojeći fajl ostaje,
   pristigli se upisuje kao `ime.conflict-{user}-{timestamp}.ext` i svi ga
   dobijaju. Događaj `conflict`.
4. **Izmena obrisanog fajla**: fajl se vraća sa novim sadržajem; obrisana
   verzija je već u trash-u.
5. **Brisanje izmenjenog fajla**: base_hash brisanja se razlikuje od trenutnog —
   brisanje se odbija, klijent preuzima novu verziju.
6. **Brisanje**: fajl ide u trash (`deleted`). Brisanje foldera je brisanje
   svakog fajla u njemu, pojedinačno zabeleženo, pa `rmdir`.
7. **Premeštanje**: `move` ne prenosi sadržaj. Klijent ga prepoznaje kad
   nestali i novi fajl imaju isti hash.
8. **Folderi** su zapisi u dnevniku (`mkdir`/`rmdir`), pa se i prazni
   sinhronizuju.

### Imena

- Putanje su relativne, sa `/`, u Unicode NFC obliku. `..`, apsolutne putanje i
  kontrolni znakovi se odbijaju.
- Server odbija ime koje se od postojećeg razlikuje samo po veličini slova
  (klijent ga tretira kao konflikt), kao i znakove i imena koja Windows ne
  dozvoljava (`< > : " | ? *`, `CON`, `NUL`, završna tačka ili razmak), da
  folder ostane upotrebljiv kad stigne Windows klijent.

## 7. `.shfignore`

Ista sintaksa i logika kao `.gitignore`:

- obrasci sa `*`, `**`, `?`, `[...]`, završno `/` za foldere, vodeće `/` za
  sidrenje, `!` za izuzetak, `#` za komentar;
- može ih biti više, na različitim nivoima; svaki važi od svog foldera naniže,
  a dublji ima prednost;
- `.shfignore` se sinhronizuje kao svaki drugi fajl, pa pravila važe za sve
  članove. Ako je naveden u samom sebi, ostaje lokalan samo za tog člana.

Ponašanje:

1. Ignorisana putanja se ne šalje, a lokalne promene i brisanja na njoj se ne
   prijavljuju.
2. **Ignorisanje nikad nije brisanje.** Ako se ignoriše fajl koji je već na
   serveru, on tamo ostaje i ostali ga i dalje imaju; klijent samo prestaje da
   ga prati. Da bi nestao svima, mora se prvo obrisati.
3. Ignorisana putanja se ni ne preuzima: ako je fajl na serveru, a kod mene
   ignorisan (lokalni `.shfignore`), klijent ga ne skida i ne dira lokalni
   fajl istog imena. (Odlučeno 2026-11-23 u Planning chatu.)
4. Kad u istoj grupi promena stigne i nov `.shfignore`, primenjuje se prvi.
5. Pravila se primenjuju samo na klijentu. Server ih ne tumači.
6. Ispod `.shfignore` postoji ugrađena lista (`.DS_Store`, `Thumbs.db`,
   `desktop.ini`, `~$*`, `*.swp`, `*.tmp`, `*.part`, `*.crdownload`) koja se
   može poništiti sa `!`. Privremeni fajlovi samog klijenta ignorišu se uvek.

## 8. Klijent

### 8.1 Lokalno stanje

SQLite van deljenog foldera (`$XDG_STATE_HOME/shf/<share-id>/state.db`, tj.
`~/.local/state/shf/…`; na macOS-u `~/Library/Application Support/shf/…`):
poslednji primenjeni seq i manifest — za svaku putanju kind, hash, veličina i
mtime kakvi su bili posle poslednjeg synca. Poređenjem tri stanja (lokalni
disk, manifest, dnevnik servera) klijent razlikuje „ja sam dodao“ od „drugi je
obrisao“.

Implementacija (TCKTJMT57D): pored manifesta `state.db` drži i stanje servera
izgrađeno iz dnevnika (`remote`, do `remote_seq`), probleme iz poslednjeg
ciklusa i lokalni log ciklusa. Putanja koja je ovde ignorisana vodi se u
manifestu kao `skipped` (sa serverovom verzijom), pa se kad ignorisanje
prestane fajl preuzima umesto da se protumači kao lokalno brisanje. Folder
koji je server obrisao, a lokalno sadrži samo nesinhronizovane (ignorisane)
fajlove, ostaje na disku kao `local` i ne šalje se nazad. Token
(`token`), `config.json` i SSH ključ člana stoje pored baze, sa pravima 0600.
Događaji servera nose `size` i `mtime`, pa preuzet fajl dobija mtime sa
servera.

### 8.2 Ciklus sinhronizacije

1. Preuzmi događaje posle poslednjeg seq-a.
2. Skeniraj lokalni folder (uz `.shfignore`) i uporedi sa manifestom; hash se
   računa samo kad se promene veličina ili mtime.
3. Primeni promene sa servera koje se ne sudaraju sa lokalnim; sudare reši po
   odeljku 6 (lokalna izmena se šalje i postaje konflikt kopija).
4. Pošalji lokalne promene.
5. Upiši manifest i seq; javi serveru rezultat za `sync_log`.

Preuzimanje ide u privremeni fajl u istom folderu, pa atomski rename.

Implementacija (TCKTJMT57D): ciklus se sastoji od rundi (najviše 5); runda
preuzme dnevnik, primeni pristigle `.shfignore` fajlove, skenira, napravi plan
(`client.Plan`, čista funkcija nad tri stanja) i primeni ga, a ciklus se
završava prvom rundom bez posla. Ciklus koji dostigne pet rundi samo zato što
drugi članovi stalno menjaju server, a sve što je planirao je urađeno, nije
neuspeh (TCK292YM96): novije promene uzima sledeći ciklus. Lokalna izmena u
sudaru postaje konflikt kopija tako što se lokalni fajl preimenuje u ime koje
je server dodelio (bez ponovnog preuzimanja), a serverova verzija se preuzima
na originalno ime.
Fajl čiji je mtime u trenutku upisa u manifest mlađi od 2 s upisuje se sa
mtime 0, pa se sledeći put ponovo hešira (izmena iste veličine u istom
otkucaju sata se ne gubi).

### 8.3 Pozadinski rad

- Praćenje foldera (inotify / FSEvents) sa odlaganjem; fajl se šalje tek kad mu
  veličina i mtime miruju nekoliko sekundi.
- WebSocket ka serveru; na `{"head": N}` veće od lokalnog pokreće ciklus.
- Bez WebSocket-a: poll na 60 s. Puno poređenje sa serverom jednom dnevno i pri
  svakom pokretanju.
- Pokretanje: systemd user servis na Linux-u, launchd na macOS-u.

Implementacija (TCK5FHY9NF): `shf daemon` (`internal/daemon`) služi sve
pridružene foldere jednim procesom. WebSocket se ponovo povezuje sa rastućim
razmakom (1 s do 60 s, slučajno odstupanje); dok ne radi, poll HEAD-a na 60 s.
Praćenje foldera ide preko fsnotify-a (inotify; na macOS-u kqueue), poštuje
`.shfignore` i skuplja događaje 2 s; folder koji se stalno menja ipak se
sinhronizuje najkasnije 20 s (10 × odlaganje) posle prve promene. Fajl se šalje
kad mu je mtime star 3 s ili kad je viđen sa istom veličinom i mtime-om pre 3 s;
do tada ciklus taj upload preskače i javlja kad da proba ponovo. Isto važi za
brisanje (TCK292YM96): šalje se kad folder iz kog je fajl nestao (najbliži koji
još postoji) miruje 3 s ili kad je fajl viđen kao nestao pre 3 s, pa se
premeštanje u nov folder koje je skeniranje uhvatilo napola ipak prepozna kao
`move`. Puno poređenje
(pri pokretanju i na 24 h) ponovo gradi stanje servera iz celog dnevnika.
Klijent pamti šta je sam upisao na disk (`Options.OnWrite`), pa događaji
praćenja za te putanje ne pokreću ciklus. Intervali i pragovi su u
`<state home>/settings.json`. Na SIGTERM tekuća stavka se završava (najduže
60 s), pa proces izlazi. Server šalje `{"head": N}` po povezivanju i posle
svakog upisa koji je dodao događaje, pinguje na 30 s, a opoziv člana zatvara
njegove veze (u istom procesu odmah, iz `shfd member revoke` u roku od 10 s);
isto važi za nov token (`shfd member reset-token`), jer stari tada prestaje da
važi.

### 8.4 Zaštita od masovnog brisanja

Ako bi ciklus poslao brisanje više od 20 fajlova ili više od 10% foldera,
klijent ne šalje ništa od brisanja, prelazi u stanje `blocked` i to upisuje u
log. `shf status` prikazuje šta čeka, `shf confirm` pušta brisanja, `shf
restore-local` vraća fajlove sa servera. Desktop obaveštenje ili prozor dolazi
sa tray aplikacijom, u kasnijoj fazi. Prvo povezivanje sa nepraznim lokalnim
folderom samo spaja: ništa se ne briše ni lokalno ni na serveru.

Implementacija (TCK5FHY9NF): prag je manji od „više od 20 fajlova“ i „više od
10 % sinhronizovanih fajlova foldera“ (oba u `settings.json`, negativna
vrednost isključuje prag). Broje se brisanja fajlova; `rmdir`-ovi koji idu uz
njih čekaju zajedno sa njima, a premeštanja nisu brisanja. Zadržana brisanja
stoje u `state.db` (`held_deletes`); dok ih ima, i svako novo brisanje čeka.
Blokada se skida sama kad se fajlovi vrate na disk. `shf confirm` i
`shf restore-local` čekaju da se završi ciklus pozadinskog klijenta, pa
pokreću svoj. Koren koji ne postoji, nije direktorijum ili se ne može čitati
daje `ErrFolderUnavailable` i ciklus ne radi ništa.

### 8.5 Komande

`shf join <url> <share-id> <token> <folder>`, `shf sync` (jedan ciklus),
`shf daemon`, `shf status`, `shf confirm`, `shf restore-local`, `shf log`.

Postoje (TCKTJMT57D): `join` (uz `--ssh user@host[:port]` kad treba zameniti
ono što server javlja preko `shfd serve --ssh-endpoint`), `sync`, `status`,
`log`; folder je podrazumevano tekući direktorijum ili bilo koji ispod
pridruženog foldera. Dodato (TCK5FHY9NF): `daemon`, `confirm`,
`restore-local`, `service install|uninstall|status`. Dodato (TCKERYXRJ2):
`update [--check] [--server URL] [--yes]` (klijent prati verziju svog
servera, i sam u `shf daemon`-u), `version [--short]` sa verzijom servera
([install.md](install.md#ažuriranje-shf-update)).

Admin (na serveru): `shfd serve`, `shfd share create`,
`shfd member add|revoke|set-role|reset-token|list`,
`shfd trash list|restore|purge`, `shfd verify [--repair]`, `shfd stats`.

Postoje (TCKTHRC7PQ): `trash list|restore|purge`, `verify [--repair]`,
`stats [--json]`, sa opisom i primerima u [admin.md](admin.md).

## 9. Van prve verzije

Windows klijent, tray/GUI i desktop potvrde, izbor podfoldera, HTTPS transport,
uloga samo za čitanje, automatsko čišćenje trash-a, hostovana usluga za više
kupaca, end-to-end enkripcija.

## 10. Predloženi tiketi

1. **Osnova projekta i zajednički paketi** — Go modul, raspored repozitorijuma,
   `make build/test/lint`; paketi za normalizaciju i proveru putanja, hash i
   `.shfignore` (odeljci 6 „Imena“ i 7), sa testovima.
2. **Server: folderi, članovi, dnevnik i pravila** — `registry.db`,
   `service.db`, kontrolni API bez WebSocket-a, pravila iz odeljka 6, trash,
   `service.log`. Testira se kroz API sa lokalnim transportom.
3. **Transport: rsync preko SSH-a** — interfejs `Transport`, `rsync-gate`,
   upravljanje `authorized_keys`, nastavljanje prekinutog prenosa (odeljak 5).
4. **Klijent: lokalno stanje i jedan ciklus synca** — `shf join`, `shf sync`,
   `shf status`, manifest, skeniranje sa `.shfignore`, poređenje tri stanja
   (8.1, 8.2).
5. **Klijent u pozadini i obaveštavanje** — WebSocket na serveru i klijentu,
   praćenje foldera, rezervni poll, systemd/launchd, zaštita od masovnog
   brisanja (8.3, 8.4).
6. **Admin komande** — članovi, trash (lista, vraćanje, čišćenje), `verify`,
   `stats`.
7. **Testovi s kraja na kraj** — server i dva klijenta u privremenim folderima;
   scenariji: konflikt, izmena protiv brisanja, povratak posle dužeg odsustva,
   `.shfignore`, prekinut prenos, masovno brisanje.

Redosled: 1 → 2 → 3 i 4 → 5 → 7; tiket 6 posle tiketa 2.

## 11. Otvoreno

- Radna imena `shf`, `shfd`.
- ~~Sistemski sshd sa prinudnom komandom (predlog) ili SSH server ugrađen u
  `shfd`.~~ Prva verzija koristi sistemski sshd sa prinudnom komandom
  `shfd rsync-gate` (TCKXT59YRT, [server-ssh.md](server-ssh.md)).
