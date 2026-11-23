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
    service.db                SQLite
    service.log               čitljiv ispis događaja
```

### service.db

- `members`: id, username (jedinstven u folderu), token_hash, ssh_pubkey, role
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

`events` je izvor istine; `service.log` je njegov čitljiv ispis, plus jedan red
po sinhronizaciji iz `sync_log`.

### Trash

Ime u trash-u: `<originalno ime>.deleted-by-{username}-{timestamp}` ili
`.replaced-by-{username}-{timestamp}`, timestamp u UTC (`20261123T145846Z`).
Trash je ravan direktorijum; originalna putanja je u tabeli `trash`, pa se zna
gde se fajl vraća. Ako dva unosa dobiju isto ime, dodaje se `-2`, `-3`.

## 4. Kontrolni API (HTTPS + JSON)

Autorizacija: `Authorization: Bearer <token člana>`. TLS preko reverse proxy-ja
ili ugrađen.

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
- Admin: članovi (dodaj, opozovi), trash (lista, vrati, očisti), statistike,
  provera `storage/` prema bazi.

## 5. Prenos fajlova

Interfejs `Transport` sa dve operacije: pošalji fajl za dati upload_id i preuzmi
fajl po putanji i hash-u. Prva implementacija je rsync preko SSH-a:

- Jedan sistemski korisnik na serveru; svaki član ima svoj SSH ključ koji
  klijent napravi pri pridruživanju. U `authorized_keys` stoji sa prinudnom
  komandom (`command="shfd rsync-gate --share … --member …"`), kao kod
  gitolite-a. Opoziv člana briše taj red.
- `rsync-gate` dozvoljava samo rsync, i to: upis isključivo u
  `tmp/<upload_id>` i čitanje isključivo iz `storage/`. Klijent nikad ne piše
  direktno u `storage/`, tako da servis ostaje jedini pisac.
- Upload ide sa `--partial`, pa se prekinut prenos nastavlja.
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

SQLite van deljenog foldera (`~/.local/state/shf/<share-id>/state.db`):
poslednji primenjeni seq i manifest — za svaku putanju hash, veličina i mtime
kakvi su bili posle poslednjeg synca. Poređenjem tri stanja (lokalni disk,
manifest, dnevnik servera) klijent razlikuje „ja sam dodao“ od „drugi je
obrisao“.

### 8.2 Ciklus sinhronizacije

1. Preuzmi događaje posle poslednjeg seq-a.
2. Skeniraj lokalni folder (uz `.shfignore`) i uporedi sa manifestom; hash se
   računa samo kad se promene veličina ili mtime.
3. Primeni promene sa servera koje se ne sudaraju sa lokalnim; sudare reši po
   odeljku 6 (lokalna izmena se šalje i postaje konflikt kopija).
4. Pošalji lokalne promene.
5. Upiši manifest i seq; javi serveru rezultat za `sync_log`.

Preuzimanje ide u privremeni fajl u istom folderu, pa atomski rename.

### 8.3 Pozadinski rad

- Praćenje foldera (inotify / FSEvents) sa odlaganjem; fajl se šalje tek kad mu
  veličina i mtime miruju nekoliko sekundi.
- WebSocket ka serveru; na `{"head": N}` veće od lokalnog pokreće ciklus.
- Bez WebSocket-a: poll na 60 s. Puno poređenje sa serverom jednom dnevno i pri
  svakom pokretanju.
- Pokretanje: systemd user servis na Linux-u, launchd na macOS-u.

### 8.4 Zaštita od masovnog brisanja

Ako bi ciklus poslao brisanje više od 20 fajlova ili više od 10% foldera,
klijent ne šalje ništa od brisanja, prelazi u stanje `blocked` i to upisuje u
log. `shf status` prikazuje šta čeka, `shf confirm` pušta brisanja, `shf
restore-local` vraća fajlove sa servera. Desktop obaveštenje ili prozor dolazi
sa tray aplikacijom, u kasnijoj fazi. Prvo povezivanje sa nepraznim lokalnim
folderom samo spaja: ništa se ne briše ni lokalno ni na serveru.

### 8.5 Komande

`shf join <url> <share-id> <token> <folder>`, `shf sync` (jedan ciklus),
`shf daemon`, `shf status`, `shf confirm`, `shf restore-local`, `shf log`.

Admin (na serveru): `shfd serve`, `shfd share create`, `shfd member add|revoke|list`,
`shfd trash list|restore|purge`, `shfd verify [--repair]`, `shfd stats`.

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
