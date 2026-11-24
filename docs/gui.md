# shfd-gui: grafički prikaz servera

Status: v1 je urađen (2026-11-24, TCKDPWDT6E); kako je napravljen i kako se
instalira je u odeljku [Izrada v1](#izrada-v1) na kraju. Ostatak je
specifikacija po kojoj je rađeno. Vizuelni prototip je
[`docs/gui-prototype.html`](gui-prototype.html). Otvori ga u browseru; ekrani
su `#members`, `#logs`, `#files` i `#install`, a prazan hash je prijava.
Podaci u prototipu su izmišljeni.

## Cilj

Uz `shfd` se instalira poseban servis, **shfd-gui**, koji na korenu hosta
(`https://<host>/`) ili pod prefiksom (`https://<host>/gui`) daje (vidi
[Base path: koren ili /gui](#base-path-koren-ili-gui)):

1. **Prijavu**: admin se prijavljuje tokenom člana.
2. **Članovi i tokeni**: isto što radi `shfd member …`, samo iz browsera.
3. **Logove**: šta se dešava na folderu, uživo (jsudc live stream).
4. **Fajlove**: pregled `storage/` foldera (jsudc), uz trash.
5. **Instalaciju**: uputstvo kako se uređaj priključi, a za admina i kako se
   podiže server.

Test instalacija: `https://shared-storage.igor.digitalcube.work/` (ranije
`/gui`; stari linkovi se preusmeravaju).

## Arhitektura

```
browser ──<base>/──► nginx ──► shfd-gui (Node, 127.0.0.1:8444)
                                │  jsudc server: <base>/udc/*, SSE <base>/udc/changes
                                │  sesija: httpOnly cookie → token u memoriji servera
                                ▼
                             shfd HTTP API (127.0.0.1:8443), uvek tokenom prijavljenog člana
```

- **shfd-gui je poseban proces i poseban systemd servis** (`shfd-gui.service`).
  Ne čita `service.db` ni `service.log` direktno: sve dobija preko shfd HTTP
  API-ja, tokenom prijavljenog člana. Zato shfd i dalje sam proverava prava
  (`401` / `403`), a GUI može da radi i pod drugim korisnikom ili na drugoj
  mašini.
- **Stack:** Node 22 + TypeScript. Backend je Fastify sa `@jsudc/server`
  (dispatcher, registry, change feed). Frontend je React sa `@jsudc/react`
  (`UdcDataView`, `useUdcChanges`), build radi Vite. Paketi su sa npm-a
  (`@jsudc/core`, `@jsudc/server`, `@jsudc/react`, verzija ≥ 0.5.0).
- **Kod:** direktorijum `gui/` u ovom repou (`gui/server`, `gui/web`) i
  Makefile cilj `make gui`. Build daje jedan direktorijum za
  `/opt/shfd-gui/versions/<sha>`, isti raspored kao za shfd.
- **Base path:** sve je ispod `<base>` (`""` na korenu, ili `/gui`):
  stranice, `<base>/api/…`, `<base>/udc/<action>` i `<base>/udc/changes`.
  `UdcClient` dobija `httpUrl: <base>`, a Fastify plugin `prefix: <base>`.
  Detalji u [Base path: koren ili /gui](#base-path-koren-ili-gui).

## Šta treba dodati u shfd (Go)

Danas za članove postoji samo CLI. Admin API pokriva trash, verify i stats
(`docs/admin.md`). Nedostaje sledeće, a sve ide pod postojeći admin `wrap`
(uloga `admin`, inače `403`):

| Poziv | Šta radi | Napomena |
|---|---|---|
| `GET /v1/whoami` | za `Authorization: Bearer shf_…` vraća `{share_id, share_name, username, role}` | Token je vezan za jedan folder, pa je dovoljan za prijavu. Nepoznat ili opozvan token dobija `401`. Ne traži ulogu admin. |
| `GET /v1/shares/{id}/admin/members` | lista kao `shfd member list`, sa SSH otiskom (`ssh_fingerprint`) | podaci kao `server.Stats.members` + otisak |
| `POST /v1/shares/{id}/admin/members` | `{username, admin}` → nov član ili vraćanje opozvanog; odgovor nosi `token` **samo ovaj put** | greške kao u CLI-ju: `member_exists` |
| `POST …/admin/members/{username}/revoke` | opoziv | `unknown_member`, `last_admin` |
| `POST …/admin/members/{username}/role` | `{role: "admin"\|"member"}` | `unknown_member`, `last_admin` |
| `POST …/admin/members/{username}/reset-token` | nov token, vraća se samo tada | `unknown_member` |
| `GET /v1/shares/{id}/admin/log?after=<offset>&limit=<n>` | parsirani redovi `service.log` od bajt-offseta: `{offset, next_offset, lines:[{ts, type, seq?, member, op, path, old_path?, dir?, outcome?, detail?, raw}]}` | `type` ∈ `event`, `transfer`, `key-replaced`. Bez `after` vraća poslednjih `limit` redova. Linija koja se ne može parsirati ide kao `type: "other"` sa `raw`. |
| `GET /v1/shares/{id}/admin/files?dir=<putanja>` | jedan nivo iz tabele `files`: `{path, name, kind, size, mtime, hash, seq, updated_by}` | Sortirano: prvo folderi, pa po imenu. Bez čitanja diska. |

Pravila:

- Članovi se menjaju istom logikom kao `shfd member …`, ista funkcija za CLI i
  API, bez duplikata. Posledice ostaju iste: WebSocket veze opozvanog člana
  se zatvaraju, a stari token odmah dobija `401`.
- Token se nikad ne loguje i nikad se ne vraća osim u odgovoru na
  `add` / `reset-token`.
- Testovi idu u `internal/server/httpapi` po uzoru na `admin_test.go` i
  `members_test.go`: uloge `401`/`403`, `last_admin`, vraćanje opozvanog
  člana, `log` sa `after` i bez njega, `files` za koren i podfolder.
- Dopuni `docs/admin.md` (odeljak „Admin API“).

## shfd-gui: ekrani

Izgled je kao u prototipu: svetla tema, leva navigacija, zaglavlje sa
folderom, statusom servera, prijavljenim članom i dugmetom za odjavu. UI je
na srpskom (latinica). jsudc i18n neka ostane na `sr`, ako ga ima.

### Prijava (`<base>/`)

Od TCKJHR5PHM prijava je **nalogom** (username i lozinka); vidi
[Nalozi u GUI-ju](#nalozi-u-gui-ju). Prijava tokenom iz v1, opisana
ispod, ostala je kao skrivena opcija.

- Jedno polje, **token**. GUI zove `GET /v1/whoami`. Na `401` ispisuje
  „Token nije ispravan ili je opozvan“. Ako je `role != admin`, ispisuje
  „Samo admin foldera može da koristi GUI“ i ne pravi sesiju.
- Sesija: `httpOnly`, `Secure`, `SameSite=Strict` cookie sa nasumičnim id-om.
  Token je samo u memoriji servera (mapa id → token, share). Ističe posle 12 h
  neaktivnosti, a restart servisa odjavljuje sve. Token nikad ne ide u
  `localStorage` niti u URL.
- Strana Instalacija je dostupna i bez prijave, preko linka sa ekrana za
  prijavu.

### Članovi i tokeni

- jsudc kolekcija `members` (Table). Kolone: član, uloga, status (aktivan /
  čeka join = nema SSH ključa / opozvan), poslednji put viđen, poslednji upis,
  kasni (`behind`, istaknuto kad je > 0) i SSH otisak (skraćen, ceo u
  tooltipu).
- Filteri: Svi, Aktivni, Opozvani, Admini. Pretraga po imenu.
- Akcije: **Nov član** (ime + checkbox admin), **Nov token**, promena uloge,
  **Opozovi** (uz potvrdu) i **Vrati člana** (kod opozvanog).
- Posle `add` ili `reset-token` iznad tabele stoji žuto polje sa tokenom i
  tekstom „Sačuvaj ga sada, čuva se samo hash i neće se ponovo prikazati“.
  Ima dugmad **Kopiraj** i **Kopiraj `shf join` komandu**
  (`shf join https://<host> <share-id> <token> <folder>`, sintaksa iz
  README-a). Polje nestaje na Zatvori ili kad se napusti strana.
- Greške iz API-ja (`member_exists`, `last_admin` …) se prikazuju kao
  rečenica, ne kao kod.
- Tekst u zaglavlju: „Jedan član = jedan uređaj.“

### Logovi

- jsudc kolekcija `logs` (Table, samo čitanje), izvor `admin/log`. Kolone:
  vreme, tip (izmena / transfer / ključ / odbijeno), član, operacija (boja po
  operaciji), putanja i seq. Najnoviji red je gore.
- **Live:** shfd-gui za svaki folder sa aktivnom sesijom jednom u sekundi
  pita `admin/log?after=<next_offset>`. Kad stignu novi redovi, zove
  `changes.notify("logs", { op: "insert" })`, a UI se osvežava preko jsudc
  change feeda (SSE). U zaglavlju stoji oznaka „live“ / „reconnecting“, koju
  jsudc daje sam, i dugme Pauziraj, koje samo zamrzava prikaz.
- Filteri: Sve, Izmene, Transferi, Ključevi, Greške (`outcome != ok`), plus
  pretraga po putanji, članu i operaciji.
- Pločice iznad tabele: broj događaja danas, transferi (upload/download),
  odbijeni pokušaji i povezani klijenti. Ako shfd nema podatak za povezane
  klijente, ta pločica se izostavlja (ne izmišljati).
- Sa strane: aktivnost po članu u poslednjih sat vremena (iz `logs`) i
  kartica Server (verzija, head seq, zauzeće, trash, iz `admin/stats` i
  `/healthz`).

### Fajlovi

- jsudc kolekcija `files`, kao primer
  <https://examples.jsudc.dev/udc/files>: putanja foldera gore, klik na
  folder ulazi u njega, pretraga, filter po ekstenziji i oznaka „live“.
  Kolone: ime, veličina, izmenjeno, izmenio (`updated_by`) i hash (skraćen).
  Izvor je `admin/files?dir=`.
- Live: kad u `logs` stigne `event` red sa `op` ∈ put/delete/move/mkdir/restore,
  GUI zove `changes.notify("files")`.
- **Prvo proveriti** da li jsudc ima gotov izvor ili prikaz za fajlove (primer
  `examples.jsudc.dev/udc/files` postoji; izvor primera je na npm-u ili u
  repou jsudc). Ako ima, koristi se on sa našim izvorom podataka. Ako nema,
  koristi se Table renderer sa putanjom foldera iznad tabele.
- **Sličice i ikonice.** Uz ime svakog fajla stoji sličica (slike i
  PDF-ovi, naslovna strana; pravi ih `shfd thumbs`, [admin.md](admin.md#thumbnails))
  ili ikonica tipa: list papira sa presavijenim uglom i obojenom pločicom,
  **W** plavo (Word: doc, docx, odt, rtf), **X** zeleno (Excel: xls, xlsx,
  ods, csv), **P** narandžasto (PowerPoint: ppt, pptx, odp), **PDF** crveno,
  `</>` za kod, **ZIP** za arhive, znak slike, zvuka i videa, linije za
  tekst; ostalo je prazan list, folder je plavi folder. Naš crtež, ne logo
  proizvođača. Dok sličica nije napravljena stoji ikonica tipa; kad stigne,
  sama je zameni, bez osvežavanja strane.
- **Tabela i Mreža.** Dva jezička iznad trake alata. U Tabeli je sličica
  32 px pored imena (redovi iste visine kao ranije). Mreža su pločice:
  kvadrat sa sličicom ili ikonicom, ispod ime (do dva reda, celo ime na
  prelaz mišem), veličina i datum; folderi su prvi, `..` je pločica. Klik
  ili Enter na folder ulazi u njega (fokus prelazi na prvu pločicu, pa se
  kroz stablo ide samo tastaturom). Pretraga, filter po ekstenziji, putanja
  foldera i straničenje su isti u oba prikaza.
- **Klizač „Veličina sličica“** (samo u Mreži, desno u traci alata): 64–320
  px, podrazumevano 144, dupli klik vraća 144. Menja veličinu odmah, bez
  novog učitavanja liste; prema veličini (i gustini ekrana) bira se sličica
  `s` (96 px), `m` (256) ili `l` (512). Ispod 112 px pločica ima samo ime.
  Veličina i poslednji jezičak (Tabela/Mreža) pamte se u browseru
  (`localStorage`).
- **Uživo.** Dok trenutni prikaz ima fajl čija sličica „čeka“ (`pending`),
  lista se ponovo učitava na 3 s, najduže 2 min za isti skup takvih fajlova
  (nov folder, pretraga ili promena skupa počinje iznova); bez njih nema
  dodatnih zahteva. Pauza „uživo“ zaustavlja i ovo. Fajl koji stigne dok
  je strana otvorena dolazi preko change feeda, sa ikonicom, pa mu sličica
  stiže sama.
- Filter **Ekstenzija** nudi ekstenzije iz tabele tipova (jpg, pdf, docx,
  xlsx, zip, txt, …); jsudc select čip ne čita vrednosti iz podataka.
- **Pregled i preuzimanje.** Klik na ime fajla (Tabela) ili na pločicu
  (Mreža; i Enter) otvara pregled: slika u većoj veličini (prvo sličica
  `m` od 256 px ako je ima, pa original preko nje), tekst
  (`txt/md/csv/log/json`) kao tekst, prvih 200 KB (zahtev sa `Range`),
  video (`mp4/m4v/webm/ogv/mov`) i zvuk (`mp3/m4a/aac/wav/ogg/oga/opus/flac`)
  u plejeru browsera (`Range`, pa premotavanje radi), PDF
  kao dugme **Otvori** (nov tab), a sve ostalo podaci fajla (putanja,
  veličina, izmenjen, poslao, hash) i dugme **Preuzmi**. **Preuzmi** je u
  svakom pregledu (`?download=1`, uvek attachment). `html`, `svg`, `xml`,
  `js` i ostali tipovi van bele liste se nikad ne prikazuju u browseru,
  samo preuzimaju — i kad se link otvori direktno.
- **Kontekstni meni** (TCKXT9D9DJ). Desni klik na red Tabele ili pločicu
  Mreže (i taster menija / Shift+F10 na fokusiranom imenu ili pločici)
  otvara meni kod pokazivača. Fajl: **Informacije** (ime, tip, putanja,
  veličina i u bajtovima, izmenjen, poslao, seq, ceo hash), **Prikaži**
  (pregled iznad; sivo za tip bez pregleda), **Preuzmi**, **Preimenuj** i
  **Obriši**. Folder: **Otvori**, **Informacije** i **Obriši** (samo prazan
  folder; shfd folder ne preimenuje). `..` nema meni. Strelice, Home/End,
  Enter i Esc rade u meniju.
- **Preimenuj i Obriši** su host akcije `shfd.file_rename` i
  `shfd.file_remove`, koje zovu isti `POST /v1/shares/{id}/ops` kao uređaj
  (`move`, `delete`, `rmdir`), tokenom prijavljenog admina. Zato važe
  pravila sync-a: `base_hash` je verzija koju GUI prikazuje (fajl izmenjen u
  međuvremenu → `stale_base`, „osveži prikaz“), obrisan fajl ide u trash
  (vraća se karticom **Trash**), a svi uređaji foldera dobiju događaj i
  urade isto. Preimenovanje menja samo ime u istom folderu (bez `/` i `\`);
  zauzeto ime je `dest_exists`. Nema upload-a.
- Kartica **Trash**: lista iz postojećeg `GET …/admin/trash` (putanja, razlog,
  ko, kad, veličina) i akcija **Vrati** (`POST …/trash/{id}/restore`), uz
  poruku koju API vrati (npr. `.restored-…` ime). **Purge nije u v1**, jer
  nepovratno briše.

### Instalacija

Strana bez prijave, dve kartice:

- **Klijent (shf):** instalacija (Linux / macOS / Windows; gotov paket iz
  `make release` ili build iz izvornog koda, po [install.md](install.md); na
  macOS-u skidanje karantina sa `xattr`). Za gotov paket strana pita shfd
  (`GET /v1/downloads`, bez tokena) i daje link „Preuzmi …“ na
  `/downloads/<ime>` za izabrani OS (na macOS-u i Apple čip / Intel), link
  na `SHA256SUMS` i komande sa tačnim imenom i SHA-256 paketa; kad shfd nema
  `--downloads` ili paketa za tu platformu, kaže to i upućuje na admina.
  Zatim `shf join …`, `shf service install`, `shf status` i napomena o „key refused“ (jedan član = jedan
  uređaj). Ime hosta se popunjava iz trenutnog URL-a. U v1 nema `install.sh`;
  ako ga dodamo kasnije, ova strana ga prikazuje.
- **Server (shfd + shfd-gui):** sažetak iz `docs/server-ssh.md` i odeljka
  „Instalacija“ ispod.

## Instalacija i deploy shfd-gui

- `shfd-gui.service`, primer u `gui/deploy/shfd-gui.service`:
  `User=shfd`, `ExecStart=/opt/shfd-gui/current/bin/shfd-gui`, okruženje
  `SHFD_URL=http://127.0.0.1:8443`, `SHFD_GUI_LISTEN=127.0.0.1:8444` i
  `SHFD_GUI_BASE` (prazno = koren hosta, ili `/gui`).
- nginx: `gui/deploy/nginx-gui-root.conf` (ceo vhost, GUI na korenu) ili
  `gui/deploy/nginx-gui.conf` (snippet, GUI pod `/gui`); vidi
  [Instalacija na server](#instalacija-na-server).
- Deploy je isti kao kod shfd: `versions/<sha>`, symlink `current`, restart;
  posle prvog postavljanja to radi `make deploy` ([deploy.md](deploy.md)).

## Kriterijumi za prihvatanje

1. Na test serveru `https://shared-storage.igor.digitalcube.work/gui`
   prikazuje prijavu. Admin token ulazi, member token dobija poruku i ne ulazi,
   a pogrešan token dobija `401` poruku.
2. Dodavanje člana iz GUI-ja daje token koji radi u `shf join`. Opoziv i
   reset tokena su odmah vidljivi u `shfd member list`. Oduzimanje uloge
   poslednjem adminu je odbijeno sa jasnom porukom.
3. Promena fajla na klijentu se pojavi u Logovima za ≤ 3 s bez osvežavanja
   strane, a u Fajlovima posle toga.
4. Fajlovi: ulazak u podfolder, povratak preko putanje foldera, pretraga.
   Isto u Mreži; sličice slika i PDF-ova, ikonice ostalih tipova; klizač
   menja veličinu odmah.
   Trash: vraćanje unosa.
5. `go test ./...` prolazi uz nove testove admin API-ja. Za `gui/` važe
   `npm run build` i `npm test` (bar testovi parsiranja log reda i sesije).
6. Na tiketu su screenshot-ovi svakog ekrana sa test servera.

## Van v1

Purge trash-a, `verify` iz GUI-ja, više foldera u jednoj sesiji (promena
foldera = nova prijava), upload i preuzimanje fajlova, `install.sh`.

## Izrada v1

### Kod

```
gui/
  shared/actions.ts       host akcije (jsudc defineHostAction), dele ih server i browser
  server/src/
    main.ts               ulaz; okruženje SHFD_URL, SHFD_GUI_LISTEN, SHFD_GUI_BASE, SHFD_GUI_WEB, SHFD_GUI_TOKEN_LOGIN
    app.ts                Fastify: <base>/api/login|logout|session|healthz, jsudc plugin (prefix <base>),
                          statički web (index.html dobija <base href> i <meta name="shfd-gui-base">)
    sessions.ts           sesije u memoriji (cookie shfd_gui_sid, 12 h neaktivnosti)
    shfd.ts               klijent shfd HTTP API-ja + srpske rečenice za kodove grešaka
    collections.ts        jsudc kolekcije members, logs, files, trash (ArraySource + share_id scope)
  shared/base.ts          normalizeBase (""/"/" = koren, "/gui"), isShfdPath (/v1/, /healthz, /downloads/)
  shared/filekinds.ts     tabela ekstenzija po tipu (ikonice u web-u, opcije čipa Ekstenzija na serveru)
  shared/rename.ts        renameTarget: nova putanja preimenovanog fajla (akcija i dijalog)
    logs.ts               bafer service.log-a po folderu, poller (1 s), redovi tabele, pločice
  server/test/            vitest: sesije, parsiranje/mapiranje log reda, poller, login i akcije, ruta sličica (lažni shfd)
  web/                    React + @jsudc/react, Vite; ekrani Prijava, Članovi, Logovi, Fajlovi, Instalacija
    src/filekind.ts       fileKind(name, kind), pickVariant(cssPx, dpr)
    src/FileIcon.tsx      ikonica tipa (SVG), src/Thumb.tsx sličica preko ikonice
    src/ThumbGrid.tsx     renderer „thumbs“ (Mreža), klizač, tabela sa osvežavanjem
    src/thumbsize.ts      veličina pločica i poslednji jezičak (localStorage)
    src/liveThumbs.ts     ponovno učitavanje dok ima pending sličica
    src/FileMenu.tsx      kontekstni meni Fajlova + dijalozi Informacije, Preimenuj, Obriši (i pregled)
    src/filemenu.ts       stavke menija po redu, tip rečima, položaj menija, taster menija
  deploy/                 shfd-gui.service, nginx-gui-root.conf (GUI na korenu), nginx-gui.conf (GUI pod /gui)
  scripts/build-server.mjs  esbuild: ceo server u jedan dist/server.js (bez node_modules na serveru)
```

```sh
make gui        # = cd gui && npm ci && npm run build  →  gui/dist/{bin/shfd-gui,server.js,web/,VERSION}
make gui-test   # = cd gui && npm ci && npm test
cd gui && npm run dev:server   # razvoj: shfd-gui na 127.0.0.1:8444 (cookie bez Secure)
cd gui && npm run dev:web      # Vite na :5174, proxy <base>/api i <base>/udc ka 8444
                               # (oba čitaju isti SHFD_GUI_BASE, podrazumevano /gui)
```

### Kako radi

Putanje ispod su napisane za `/gui`; na korenu hosta su iste bez `/gui`
(`/api/login`, `/udc/changes`, …).

- **Prijava.** `POST /gui/api/login {token}` zove `GET /v1/whoami`. `401` →
  „Token nije ispravan ili je opozvan.“; `role != admin` → „Samo admin foldera
  može da koristi GUI.“, bez sesije. Inače sesija: nasumičan id (256 bita) u
  cookie-ju `shfd_gui_sid` (`HttpOnly; SameSite=Strict; Secure; Path=<base>/`,
  dakle `Path=/gui/` ili `Path=/`; odjava ga briše sa istim `Path`),
  token samo u memoriji procesa. Kad shfd za token kaže `401` (opozvan,
  resetovan) sve sesije sa tim tokenom se brišu; kad admin sebi oduzme ulogu
  ili se opozove, njegova sesija se završava.
- **jsudc.** Plugin je registrovan sa `prefix: "/gui"`, pa su akcije na
  `/gui/udc/<action>`, stream na `/gui/udc/changes`, a `UdcClient` ima
  `httpUrl: "/gui"`. Swagger je isključen. Svaki red nosi `share_id`, a
  `rowScope` drži svaku sesiju na njenom folderu (i u change feedu).
  Kolekcije su `ArraySource` (filter, sort, paging u memoriji); izvor
  podataka je shfd API tokenom sesije.
- **Fajlovi.** jsudc nema poseban renderer za fajlove; folder navigacija
  (putanja foldera, klik na folder, `..`) je deo `UdcDataView` + table
  renderera kad view ima `folder_options`, isto kao primer
  `examples.jsudc.dev/udc/files`. Izvor `files` iz filtera vadi
  `path eq <folder>` i zove `admin/files?dir=`. Pretraga po imenu, filter po
  ekstenziji. Red ima i `thumb`
  (`ready`/`pending`/`failed`/`""`, iz `admin/files`) i `hash_full`, ključ
  sličice.
- **Sličice: `GET /gui/api/thumb/{hash}?size=s|m|l`.** Ruta shfd-gui-ja (nije
  pod `/gui/udc`; nginx je pokriva kroz `location /gui/`). Traži sesiju
  (`401`), sama proverava hash (`^[0-9a-f]{64}$`) i veličinu (`400`), pa zove
  `GET /v1/shares/{id}/admin/thumbs/{hash}?size=` tokenom i folderom sesije.
  Prosleđuje `200` (telo, `content-type`, `etag`, `cache-control:
  private, max-age=31536000, immutable`), `304` (šalje dalje `if-none-match`)
  i `404` (sa `cache-control: no-store`, da browser ne zapamti „nema“);
  `401` od shfd-a gasi sesije tog tokena, kao kod ostalih poziva, a sve
  ostalo je `502`. Ključ je hash sadržaja, pa browser jednom učitanu sličicu
  više ne traži.
- **Sadržaj fajla: `GET /gui/api/files/content?path=<putanja>[&download=1]`.**
  Ruta shfd-gui-ja (kao sličice, van `/gui/udc`). Bez sesije `401`, bez
  `path` `400`; inače zove `GET …/admin/files/content` tokenom i folderom
  sesije i prosleđuje `range`, `if-range` i `if-none-match`. Odgovor se
  **strimuje** (`Readable.fromWeb`, fajl se nikad ne učitava ceo u
  memoriju) sa zaglavljima shfd-a: `content-type`, `content-disposition`,
  `content-length`, `content-range`, `accept-ranges`, `etag`,
  `cache-control`, `content-security-policy`, `x-content-type-options`
  (ostala se ne prosleđuju). Status je shfd-ov (`200`, `206`, `304`,
  `416`); odbijanje (`404`, `409 file_changed`, `400`) dolazi sa porukom na
  srpskom i `code`; `401` gasi sesije tog tokena, `403` sesiju. Ako browser
  prekine preuzimanje, prekida se i zahtev ka shfd-u. Zaglavlja shfd mora da
  pošalje u roku od 120 s (prvo heširanje velikog fajla), telo nema rok.
- **Pregled (FilePreview.tsx).** jsudc-ov `folder_options.preview` ostaje
  isključen: njegov pregled čita `/udc/files/blob` filesystem izvora, koji
  ova kolekcija nema, i sliku SVG bi prikazao. Umesto njega ćelija imena
  (`filename`) i pločica Mreže otvaraju sopstveni modal preko React
  konteksta (`PreviewProvider` oko `UdcDataView`). Odluka šta se prikazuje
  (`preview.ts`, `previewKind`) je ista bela lista kao u shfd-u.
- **Keš.** Sličice drži shfd u `<data-root>/<share-id>/thumbnails/` (pravi
  ih `shfd thumbs`, po hash-u sadržaja; nisu u `storage/`, sync ih ne vidi i
  `verify` ih preskače; mogu se obrisati, napraviće se ponovo), a browser ih
  čuva zauvek (`immutable`, URL nosi hash). Sadržaj fajla browser čuva uz
  `private, no-cache`: svaki put pita, a isti `ETag` (hash) daje `304`.
- **Renderer „thumbs“ (Mreža).** Sopstveni `RendererDefinition`
  (`paging: true`, `editMode: false`, `toolbar`: klizač), registrovan samo za
  `UdcDataView` Fajlova (`renderers={…}`) zajedno sa tabelom koja osvežava
  pending sličice; `filesView.available_renderers` je `["table", "thumbs"]`,
  ostale kolekcije su samo tabela. Crta `props.data` (isti red, pretraga,
  filter i straničenje kao tabela) i ide kroz foldere preko `props.folder`
  (`isFolderRow`, `folder.open(path)`). jsudc-ov `cards` ne odgovara: ima tri
  fiksne veličine iz konfiguracije servera i za red bez slike crta inicijale.
  Ćelija imena u tabeli je `render: "filename"`; table renderer je i dalje
  obavija folder linkom (`folder_options.label_field`), pa klik na folder
  radi kao ranije, a njegova mala ikonica je sakrivena u CSS-u
  (`.files-view`), jer je zamenjuje naša. Poslednji jezičak se pamti kroz
  `urlState`: memorijski adapter (`createMemoryUrlStateAdapter`) počinje sa
  sačuvanim `tab`-om i pri svakom upisu ga čuva.
- **Logovi uživo.** Za svaki folder sa bar jednom sesijom shfd-gui jednom u
  sekundi zove `admin/log?after=<next_offset>` (tokenom poslednje korišćene
  sesije) i drži poslednjih 5000 redova. Novi redovi → `changes.notify("logs")`
  (i `members`; `files` i `trash` kad je stigla izmena fajla), browser se
  osvežava preko SSE. Pauziraj gasi stream (prikaz stoji), Nastavi ga vraća i
  povlači sve propušteno.
- **Pločice.** „Povezani klijenti“ je zbir otvorenih WebSocket veza članova
  (`connected` iz `admin/members`); ostalo se računa iz bafera loga (od
  ponoći, lokalno vreme servera) i `admin/stats` / `/healthz`.
- **Akcije nad članovima** su jsudc host akcije (`shfd.member_add`,
  `…_revoke`, `…_set_role`, `…_reset_token`, `…_reactivate`) koje zovu nove
  admin rute shfd-a. Token iz odgovora ide samo u žuto polje i nestaje na
  Zatvori ili odlaskom sa strane.

### Jezik i tema

U zaglavlju (i na prijavi i samostalnoj stranici Instalacije) su prekidač
jezika **SR | EN** i dugme svetla/tamna tema. Oba izbora pamti browser, u
ključevima jsudc-a (`udc-locale`, `udc-theme`); bez izbora GUI je na srpskom
i svetao. Skripta u `index.html` postavlja temu i `lang` pre prvog crtanja.

- **Tekstovi** stoje uz kod koji ih prikazuje, kao `tr("srpski", "English")`:
  `useTr()` iz `web/src/i18n.tsx` u komponentama, `tr` iz `web/src/lang.ts`
  u običnim modulima (`format.ts`, `filemenu.ts`, ćelije). Naslovi kolona i
  dugmad akcija redova su u `messages.ts` (sekcije `TBL_*`, `ACTION_*`), a
  jsudc ih bira po istom jeziku (`UdcI18nProvider` dobija `locale`).
- **Brojevi i datumi** se formatiraju po jeziku (`sr-Latn-RS` / `en-US`).
- **Poruke servera** (odbijanja shfd-a, prijava, preimenovanje, trash): web
  šalje `x-shfd-lang: sr|en` uz svaki poziv, a server (`server/src/lang.ts`)
  odgovara na tom jeziku.
- **Tamna tema** je klasa `dark` na `<html>` (jsudc `ThemeProvider`); boje
  GUI-ja su promenljive `--g-*` u `proto.css`, sa tamnim vrednostima pod
  `.dark`, a površine prate tamne boje jsudc tabela.

### Odstupanja od specifikacije

- `member revoke` (CLI i API) sada odbija poslednjeg aktivnog admina
  (`last_admin`), kao što spec traži za API; CLI deli istu funkciju.
- `admin/log` vraća i `size` i, po redu, `offset` (id reda). Za `transfer`
  je `op` = `dir` (`upload`/`download`/`-`), za `key-replaced` je `detail`
  `old=… new=…`.
- `service.log` ima i četvrtu vrstu reda, izveštaj klijenta posle ciklusa
  (`sync member=… from= to= sent= received= conflicts= took= [error=]`);
  `admin/log` ga vraća kao `type: "sync"`, a GUI kao tip „sync“ (neuspeli
  sync je „odbijeno“ i ulazi u Greške).
- `admin/members` vraća `{head, members}`; član ima i `status`
  (`active`/`pending`/`revoked`) i `connected`.
- Strana Instalacija nudi gotov paket (`make release`: linux-amd64,
  darwin-amd64, darwin-arm64) ili build iz izvornog koda. Paket se preuzima
  sa shfd-a (`shfd serve --downloads`, rute `/v1/downloads` i
  `/downloads/<ime>`, bez tokena; [install.md](install.md#objavljivanje-paketa-na-serveru)).
  Ove rute idu na shfd, ne kroz shfd-gui (sa GUI-jem na korenu kroz
  `location /v1/` i `location /downloads/` u `nginx-gui-root.conf`, sa GUI-jem
  pod `/gui` kroz postojeći `location /` vhost-a), pa rade isto sa obe baze;
  `npm run dev:web` ih prosleđuje na `127.0.0.1:8443`.

### Base path: koren ili /gui

Jedan build (`gui/dist`) radi i na korenu hosta i pod `/gui`; bira se samo
promenljivom `SHFD_GUI_BASE` u `shfd-gui.service`:

| `SHFD_GUI_BASE` | GUI | nginx |
|---|---|---|
| prazno ili `/` | `https://<host>/` | ceo vhost po `gui/deploy/nginx-gui-root.conf` |
| `/gui` (podrazumevano) | `https://<host>/gui/` | `gui/deploy/nginx-gui.conf` u postojeći shfd vhost |

- **Web deo** je build-ovan sa relativnim `base: "./"`. Server pri slanju
  `index.html` upisuje `<base href="<base>/">` (relativni `./assets/…`) i
  `<meta name="shfd-gui-base" content="<base>">`; `web/src/api.ts` (`BASE`,
  `guiUrl`) ga čita i svi URL-ovi (`api`, `udc`, sadržaj i sličice fajlova)
  idu preko njega. Bez meta taga baza je direktorijum strane.
- **Rute GUI-ja** su `<base>/api/…`, `<base>/udc/…`, `<base>/assets/…` i
  `<base>/` (stranica; navigacija je u hash-u, `#files`, `#logs`, …).
- **Health:** `<base>/api/healthz` → `{"service":"shfd-gui","version":…,"ok":true}`.
  Pod `/gui` i dalje radi i stari `/gui/healthz` (`{"ok":true}`, alias).
- **Bez sudara sa shfd-om na korenu.** shfd zadržava `/v1/…` (i WebSocket
  `/v1/shares/{id}/ws`), `/healthz`, `/downloads/…` i `/v1/downloads`; to
  rešava nginx (`location /v1/`, `= /healthz`, `/downloads/` → 8443, sve
  ostalo → 8444). shfd-gui te putanje ne služi ni kad do njega stignu: odgovara
  `404` (JSON), nikad `index.html`, a ni jsudc-ov `/healthz`.
- **shfd-ov `GET /`** (isti JSON kao `/healthz`) se sa GUI-jem na korenu više
  ne vidi spolja: `/` je GUI. Za proveru shfd-a koristi `/healthz`.
- **Stari linkovi** `https://<host>/gui/…` dobijaju `301` na `/…`
  (`/gui/` → `/`, hash ostaje u browseru). Pažnja: `/gui/healthz` tako
  postaje shfd-ov `/healthz`; nadzor treba prebaciti na `/api/healthz`.
- **Kolačić** sesije ima `Path=<base>/`. Pri prelasku sa `/gui` na koren
  stara sesija (cookie sa `Path=/gui/`) se ne prenosi: treba se prijaviti
  ponovo (restart shfd-gui ionako odjavljuje sve).

### Instalacija na server

Potreban je Node 22 (`apt install nodejs` na Ubuntu 26.04 daje 22.x). Na
serveru ne treba npm ni `node_modules`: `server.js` je ceo server.

```sh
# na mašini za build
make gui VERSION=$(git rev-parse --short HEAD)
tar -C gui/dist -czf shfd-gui.tgz .

# na serveru
sha=<sha>
sudo install -d /opt/shfd-gui/versions/$sha
sudo tar -C /opt/shfd-gui/versions/$sha -xzf shfd-gui.tgz
sudo ln -sfn versions/$sha /opt/shfd-gui/current
sudo cp gui/deploy/shfd-gui.service /etc/systemd/system/     # jednom
sudo systemctl daemon-reload && sudo systemctl enable --now shfd-gui
sudo systemctl restart shfd-gui                               # posle svakog novog current
```

Stream promena (`<base>/udc/changes`, SSE) ne sme da se baferuje ni na jednom
proxy-ju: shfd-gui šalje `X-Accel-Buffering: no`, a oba nginx primera taj
header ponovo dodaje (`add_header`), jer ga nginx inače ne prosleđuje dalje.
Bez toga, kad je ispred VM-a još jedan nginx (TLS na hipervizoru, kao na test
serveru), logovi stižu tek kad se bafer napuni.

nginx, GUI na korenu (`SHFD_GUI_BASE=` u `shfd-gui.service`): vhost po
`gui/deploy/nginx-gui-root.conf` (`server_name`, `listen`/TLS po hostu) umesto
dosadašnjeg, pa `sudo nginx -t && sudo systemctl reload nginx` i
`sudo systemctl restart shfd-gui`. Provera:

```sh
curl -s https://<host>/api/healthz          # {"service":"shfd-gui","version":…,"ok":true}
curl -s https://<host>/healthz              # {"service":"shfd","version":…,"ok":true}
curl -s https://<host>/v1/downloads         # JSON shfd-a
curl -sI https://<host>/gui/ | grep -i ^location   # location: /
curl -s https://<host>/ | grep shfd-gui-base       # <meta name="shfd-gui-base" content="" />
```

nginx, GUI pod `/gui` (`SHFD_GUI_BASE=/gui`): sadržaj `gui/deploy/nginx-gui.conf`
ide u `server { }` vhost-a koji već prosleđuje na `127.0.0.1:8443`, pa
`sudo nginx -t && sudo systemctl reload nginx`. Provera:
`curl -s https://<host>/gui/api/healthz` (i stari `/gui/healthz` → `{"ok":true}`).

shfd-gui mora da radi uz shfd koji ima nove admin rute (`/v1/whoami`,
`admin/members`, `admin/log`, `admin/files`), dakle shfd iz istog commita ili
novijeg. Restart shfd-gui odjavljuje sve.

## Nalozi u GUI-ju

Urađeno u TCKJHR5PHM (2026-11-24), po specifikaciji naloga §7. Gradi na
nalozima u shfd-u (`docs/admin.md`, [Nalozi](admin.md#nalozi)).

### Prijava

- Forma ima **korisničko ime i lozinku**. shfd-gui zove
  `POST /v1/auth/login {username, password, kind: "gui", device_name}`, gde
  je `device_name` oblika „gui · Firefox (Linux)“ (iz `User-Agent`). Tako
  se svaka prijava vidi kao poseban uređaj u „Moj nalog“.
- Sesija GUI-ja čuva **device token naloga**, a ne token foldera. Token je
  samo u memoriji shfd-gui procesa. Browser dobija kolačić
  `shfd_gui_sid` sa `HttpOnly; Secure; SameSite=Strict; Path=<base>/`.
- Istek prati shfd: posle **12 h** neaktivnosti, a najkasnije **7 dana**
  posle prijave (`IDLE_MS`/`MAX_AGE_MS` u `server/src/sessions.ts`, isto
  kao `GUIIdle`/`GUIMaxAge` u shfd-u). Svaki `401` od shfd-a (opozvan
  uređaj, ugašen nalog) gasi sesiju odmah, a browser prikazuje prijavu sa
  porukom da je sesija istekla ili opozvana.
- Poruke: pogrešna lozinka, nepoznat nalog i **ugašen nalog** imaju isti
  tekst („Pogrešno korisničko ime ili lozinka.“), kao što i shfd vraća isti
  `invalid_credentials`. Posle više neuspeha shfd vraća `429` sa
  `Retry-After`, a GUI ispisuje „Previše neuspelih pokušaja. Pokušaj ponovo
  za N s.“
- **`must_change`** (privremena lozinka od `shfd user add` ili
  `shfd user passwd`): posle prijave se prikazuje samo ekran „Nova lozinka“.
  Pravila (najmanje 12 znakova, ne username, različita od stare) dolaze iz
  `password_rules` u `/v1/me`, a server ih proverava ponovo. Do promene
  lozinke GUI ne daje izbor foldera ni kolekcije (`403
  password_change_required`).
- **Odjava** zove `POST /v1/auth/logout`, pa je GUI uređaj opozvan i na
  shfd-u, ne samo u GUI-ju.
- Registracije nema. Naloge pravi admin servera (`shfd user add`).

### Zaboravljena lozinka

Urađeno u TCKP7URK2X. Server strana je u
[admin.md](admin.md#zaboravljena-lozinka).

- Na Login stranici, ispod lozinke, je link **„Zaboravljena lozinka?“**
  (`#forgot`). Forma traži **korisničko ime ili email** i posle slanja
  prikazuje uvek istu, neutralnu potvrdu („Ako nalog postoji i ima email
  adresu, na nju je poslat link… Link važi 30 minuta.“). GUI zove
  `POST <base>/api/auth/forgot {login}`, a on `POST /v1/auth/forgot {login,
  lang}`, gde je `lang` jezik GUI-ja (pa je i mejl na tom jeziku).
- Link iz mejla vodi na **`#reset?token=…`**: nova lozinka dva puta
  (najmanje 12 znakova; ostala pravila proverava shfd). Stranica radi i bez
  prijave i sa njom. Posle uspeha token se briše iz adrese i istorije
  browsera, sesija se odjavljuje (shfd ionako gasi sve gui sesije naloga) i
  otvara se prijava sa porukom „Lozinka naloga X je promenjena…“ i već
  upisanim korisničkim imenom. Iskorišćen, istekao ili zamenjen link daje
  „Link za novu lozinku nije ispravan…“ sa linkom „Zatraži novi link“.
- Link sastavlja **shfd** iz svog `SHFD_PUBLIC_URL` i `SHFD_GUI_BASE`, nikad
  iz `Host` zaglavlja. GUI proxy samo prosleđuje rute.
- **Adresa klijenta**: za `auth/login`, `auth/forgot`, `auth/reset`,
  `me/password` i `me/email` shfd-gui šalje shfd-u `X-Forwarded-For` kao
  proxy: ono što je stiglo od nginx-a, pa adresu koju je sam video. shfd ga
  čita zdesna i preskače samo pouzdane proxy-je (`SHFD_TRUSTED_PROXIES`,
  podrazumevano `127.0.0.1`), pa ograničenja broje pravog klijenta, a
  browser ne može da podmetne svoju adresu. (Do sada je shfd za sve
  prijave iz GUI-ja video `127.0.0.1`.)
- Previše zahteva sa jedne adrese: „Previše zahteva sa ove adrese. Pokušaj
  ponovo za N min.“

#### Prijava tokenom (skrivena)

Ispod forme stoji link „Prijava tokenom“: stari način, gde se admin foldera
prijavljuje tokenom člana (`/v1/whoami`, samo uloga admin, jedan folder,
bez „Moj nalog“). Podešavanje u `shfd-gui.service`:

| Promenljiva | Podrazumevano | Značenje |
|---|---|---|
| `SHFD_GUI_TOKEN_LOGIN` | uključeno | `0` gasi prijavu tokenom: link nestaje, a `POST <base>/api/login` vraća `404 token_login_off`. Prijava lozinkom radi i dalje. |

Kad svi članovi pređu na naloge (spec §8, korak 5), postavi
`SHFD_GUI_TOKEN_LOGIN=0`.

### Izbor foldera

- Posle prijave GUI čita `/v1/me` i prikazuje listu **„Moji folderi“** sa
  imenom, ulogom i poslednjom promenom (`last_change_at`, vreme
  najnovijeg događaja foldera).
- Ako nalog ima **samo jedan folder**, on se otvara odmah. Izbor se pamti
  **po nalogu** u `localStorage` (`shfd-gui-folder:<username>`). Pri
  sledećoj prijavi u istom browseru odmah se otvara zapamćeni folder, ako je
  nalog i dalje njegov član. Inače se prikazuje lista.
- Izabrani folder stoji u zaglavlju („Folder stp-assets“) sa padajućim
  menijem za promenu i stavkom „Svi moji folderi…“.
- Izbor čuva server (`POST <base>/api/folder {share_id}`, posle provere u
  `/v1/me`). Sve rute foldera, i to Članovi, Logovi, Fajlovi, Trash, sadržaj
  i sličice fajlova i SSE change feed, idu u izabrani `share_id` sa device
  tokenom. Kad se folder promeni, browser ponovo otvara change feed.

### Prava po ulozi

| Ekran | admin foldera | član koji nije admin | bez izabranog foldera |
|---|---|---|---|
| Članovi, Logovi, Trash | da | ne | ne |
| Fajlovi: pregled, preuzimanje, sličice | da | da | ne |
| Fajlovi: preimenovanje i brisanje | da | ne | ne |
| Moji folderi, Moj nalog | da | da | da |
| Instalacija | da | da | da |

- Meni nudi samo dozvoljene stranice. Ako se `#members` ili `#logs` upiše
  ručno, otvara se podrazumevana stranica uloge.
- Server drži istu granicu. shfd na svakoj `admin/*` ruti vraća `403
  forbidden` članu bez uloge admin (test `TestMemberFileRoutes`), a
  shfd-gui ga prenosi kao „To može samo admin foldera.“. Preimenovanje i
  brisanje shfd-gui odbija sam, iako bi ih shfd preko `/ops` dozvolio
  članu: u GUI-ju član samo gleda i preuzima.
- Za pregled i preuzimanje GUI koristi rute za člana
  (`/v1/shares/{id}/files`, `files/content`, `thumbs/{hash}`; vidi
  `docs/admin.md`).
- Ako adminu oduzmu ulogu dok je prijavljen, sesija ostaje, ali kao član
  tog foldera. Sesija prijavljena tokenom se u tom slučaju završava, kao i
  ranije.

### Članovi

- **„Dodaj člana“** bira **nalog**. Pretraga ide preko
  `/v1/users?q=` (početak username-a ili deo imena), a zatim
  `POST admin/members {user, admin}`. Token se ne pravi.
- Stari način, „član sa tokenom (bez naloga)“, ostaje pod **„Napredno“** u
  istom dijalogu (`{username, admin}`, token se prikaže jednom).
- U tabeli su kolone **Nalog** i **Uređaji** (broj aktivnih). Član bez
  naloga ima oznaku **„bez naloga“**. „Nov token“ se nudi samo njemu.
- Akcija reda **„Uređaji“** otvara listu uređaja člana u ovom folderu: ime,
  tip (klijent / GUI sesija), SSH otisak i poslednji put viđen. Odatle se
  opoziva jedan uređaj
  (`DELETE admin/members/{član}/devices/{id}`), a ostali uređaji rade dalje.

### Moj nalog

- **Email**: adresa na koju stiže link za zaboravljenu lozinku. Promena (ili
  brisanje, prazno polje) traži trenutnu lozinku
  (`PUT <base>/api/me/email` → `PUT /v1/me/email`). Greške: „Lozinka nije
  tačna.“, „To nije ispravna email adresa.“, „Tu email adresu već ima drugi
  nalog.“
- **Promena lozinke**: stara i nova lozinka i ponovljena nova, uz pravila
  sa servera.
- **Moji uređaji**: klijenti (`shf login`) i GUI sesije naloga, sa imenom,
  tipom, vremenom prijave, istekom i poslednjim viđenjem. Trenutna sesija
  ima oznaku „ova sesija“. **Opoziv** (`DELETE /v1/me/devices/{id}`) gasi
  uređaj u svim folderima. Opoziv trenutne sesije odjavljuje browser.

### CSRF

Kolačić je `SameSite=Strict`, pa ga browser ne šalje na zahtev koji
pokrene drugi sajt. Pored toga, shfd-gui za **svaki POST, PUT, PATCH i
DELETE** ka `<base>/api/…` i `<base>/udc/…` proverava `Origin`. Ako ga
nema, proverava `Referer`. Ime hosta iz tog zaglavlja mora biti isto kao
host zahteva (`Host`, ili `X-Forwarded-Host` od proxy-ja kome se veruje).
Port se ne poredi, jer nginx prosleđuje `Host $host` bez porta. Zahtev
koji nema ni `Origin` ni `Referer` takođe se odbija. Svi browseri šalju
`Origin` uz POST iz `fetch`-a. Odbijanje je `403 {code: "csrf"}`. Kod je
u `server/src/csrf.ts`, a testovi su u `server/test/accounts.test.ts`.

### Rute shfd-gui servera (pod `<base>`)

| Ruta | Šta radi |
|---|---|
| `GET /api/config` | `{token_login}` |
| `POST /api/auth/login` | `{username, password}` → sesija naloga |
| `POST /api/login` | `{token}` → sesija tokenom (samo kad je uključena) |
| `POST /api/logout` | odjava (`/v1/auth/logout` za nalog) |
| `GET /api/session` | `{kind, username, display_name, must_change, share_id, share_name, role, member}` |
| `GET /api/me` | `/v1/me` (folderi, uređaji, pravila lozinke) i `session` |
| `POST /api/folder` | `{share_id}` → izbor foldera |
| `POST /api/me/password` | `{old, new}` |
| `POST /api/auth/forgot` | `{login}` → `202 {ok, message}`, uvek isto (bez sesije) |
| `POST /api/auth/reset` | `{token, new_password}` → `{ok, username}` (bez sesije) |
| `PUT /api/me/email` | `{email, current_password}` → `{ok, email}` |
| `DELETE /api/me/devices/:id` | opoziv svog uređaja; `{signed_out}` kad je to ova sesija |

Nove jsudc akcije: `shfd.member_add_account`, `shfd.users_search`,
`shfd.member_devices`, `shfd.member_device_revoke`.
