# shfd-gui: grafički prikaz servera

Status: v1 je urađen (2026-11-24, TCKDPWDT6E); kako je napravljen i kako se
instalira je u odeljku [Izrada v1](#izrada-v1) na kraju. Ostatak je
specifikacija po kojoj je rađeno. Vizuelni prototip je
[`docs/gui-prototype.html`](gui-prototype.html). Otvori ga u browseru; ekrani
su `#members`, `#logs`, `#files` i `#install`, a prazan hash je prijava.
Podaci u prototipu su izmišljeni.

## Cilj

Uz `shfd` se instalira poseban servis, **shfd-gui**, koji na
`https://<host>/gui` daje:

1. **Prijavu**: admin se prijavljuje tokenom člana.
2. **Članovi i tokeni**: isto što radi `shfd member …`, samo iz browsera.
3. **Logove**: šta se dešava na folderu, uživo (jsudc live stream).
4. **Fajlove**: pregled `storage/` foldera (jsudc), uz trash.
5. **Instalaciju**: uputstvo kako se uređaj priključi, a za admina i kako se
   podiže server.

Test instalacija: `https://shared-storage.igor.digitalcube.work/gui`.

## Arhitektura

```
browser ──/gui/──► nginx ──► shfd-gui (Node, 127.0.0.1:8444)
                                │  jsudc server: /gui/udc/*, SSE /gui/udc/changes
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
- **Base path:** sve je ispod `/gui`: stranice, `/gui/udc/<action>` i
  `/gui/udc/changes`. `UdcClient` dobija `httpUrl: "/gui"`. Ako jsudc ne
  podržava prefiks, Fastify plugin se registruje sa `prefix: "/gui"`.

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

### Prijava (`/gui/`)

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
- Samo čitanje: nema upload-a, brisanja ni preuzimanja sadržaja u v1.
- Kartica **Trash**: lista iz postojećeg `GET …/admin/trash` (putanja, razlog,
  ko, kad, veličina) i akcija **Vrati** (`POST …/trash/{id}/restore`), uz
  poruku koju API vrati (npr. `.restored-…` ime). **Purge nije u v1**, jer
  nepovratno briše.

### Instalacija

Statička strana, dve kartice:

- **Klijent (shf):** preuzimanje binarnog fajla (Linux / macOS / Windows,
  kako je danas u README-u), `shf join …`, `shf service install`, `shf status`
  i napomena o „key refused“ (jedan član = jedan uređaj). Ime hosta se
  popunjava iz trenutnog URL-a. U v1 nema `install.sh`; ako ga dodamo kasnije,
  ova strana ga prikazuje.
- **Server (shfd + shfd-gui):** sažetak iz `docs/server-ssh.md` i odeljka
  „Instalacija“ ispod.

## Instalacija i deploy shfd-gui

- `shfd-gui.service`, primer u `gui/deploy/shfd-gui.service`:
  `User=shfd`, `ExecStart=/opt/shfd-gui/current/bin/shfd-gui`, okruženje
  `SHFD_URL=http://127.0.0.1:8443`, `SHFD_GUI_LISTEN=127.0.0.1:8444` i
  `SHFD_GUI_BASE=/gui`.
- nginx (vhost koji već prosleđuje na 8443) dobija snippet u
  `gui/deploy/nginx-gui.conf`:
  ```nginx
  location /gui/ {
      proxy_pass http://127.0.0.1:8444;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
  }
  location /gui/udc/changes {
      proxy_pass http://127.0.0.1:8444;
      proxy_buffering off;
      proxy_read_timeout 1h;
  }
  ```
- Deploy je isti kao kod shfd: `versions/<sha>`, symlink `current`, restart.
  Sve to ide u `docs/gui.md` kod izrade.

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
   Trash: vraćanje unosa.
5. `go test ./...` prolazi uz nove testove admin API-ja. Za `gui/` važe
   `npm run build` i `npm test` (bar testovi parsiranja log reda i sesije).
6. Na tiketu su screenshot-ovi svakog ekrana sa test servera.

## Van v1

Purge trash-a, `verify` iz GUI-ja, više foldera u jednoj sesiji (promena
foldera = nova prijava), upload i preuzimanje fajlova, `install.sh`, tamna
tema.

## Izrada v1

### Kod

```
gui/
  shared/actions.ts       host akcije (jsudc defineHostAction), dele ih server i browser
  server/src/
    main.ts               ulaz; okruženje SHFD_URL, SHFD_GUI_LISTEN, SHFD_GUI_BASE, SHFD_GUI_WEB
    app.ts                Fastify: /gui/api/login|logout|session, jsudc plugin (prefix /gui), statički web
    sessions.ts           sesije u memoriji (cookie shfd_gui_sid, 12 h neaktivnosti)
    shfd.ts               klijent shfd HTTP API-ja + srpske rečenice za kodove grešaka
    collections.ts        jsudc kolekcije members, logs, files, trash (ArraySource + share_id scope)
    logs.ts               bafer service.log-a po folderu, poller (1 s), redovi tabele, pločice
  server/test/            vitest: sesije, parsiranje/mapiranje log reda, poller, login i akcije (lažni shfd)
  web/                    React + @jsudc/react, Vite; ekrani Prijava, Članovi, Logovi, Fajlovi, Instalacija
  deploy/                 shfd-gui.service, nginx-gui.conf
  scripts/build-server.mjs  esbuild: ceo server u jedan dist/server.js (bez node_modules na serveru)
```

```sh
make gui        # = cd gui && npm ci && npm run build  →  gui/dist/{bin/shfd-gui,server.js,web/,VERSION}
make gui-test   # = cd gui && npm ci && npm test
cd gui && npm run dev:server   # razvoj: shfd-gui na 127.0.0.1:8444 (cookie bez Secure)
cd gui && npm run dev:web      # Vite na :5174, proxy /gui/api i /gui/udc ka 8444
```

### Kako radi

- **Prijava.** `POST /gui/api/login {token}` zove `GET /v1/whoami`. `401` →
  „Token nije ispravan ili je opozvan.“; `role != admin` → „Samo admin foldera
  može da koristi GUI.“, bez sesije. Inače sesija: nasumičan id (256 bita) u
  cookie-ju `shfd_gui_sid` (`HttpOnly; SameSite=Strict; Secure; Path=/gui/`),
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
  ekstenziji. Bez preview-a i preuzimanja (v1).
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

### Odstupanja od specifikacije

- `member revoke` (CLI i API) sada odbija poslednjeg aktivnog admina
  (`last_admin`), kao što spec traži za API; CLI deli istu funkciju.
- `admin/log` vraća i `size` i, po redu, `offset` (id reda). Za `transfer`
  je `op` = `dir` (`upload`/`download`/`-`), za `key-replaced` je `detail`
  `old=… new=…`.
- `admin/members` vraća `{head, members}`; član ima i `status`
  (`active`/`pending`/`revoked`) i `connected`.
- Na strani Instalacija klijent se pravi iz izvornog koda (`make build`),
  jer gotovih binarnih fajlova za preuzimanje još nema.

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

nginx: sadržaj `gui/deploy/nginx-gui.conf` ide u `server { }` vhost-a koji
već prosleđuje na `127.0.0.1:8443`, pa `sudo nginx -t && sudo systemctl
reload nginx`. Provera: `curl -s https://<host>/gui/healthz` → `{"ok":true}`.

shfd-gui mora da radi uz shfd koji ima nove admin rute (`/v1/whoami`,
`admin/members`, `admin/log`, `admin/files`), dakle shfd iz istog commita ili
novijeg. Restart shfd-gui odjavljuje sve.
