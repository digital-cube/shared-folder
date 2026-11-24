# Postavljanje servera za rsync preko SSH-a

Fajlovi između klijenta `shf` i servera `shfd` idu rsync-om preko sistemskog
`sshd`-a (specifikacija, odeljak 5). Svaki član ima svoj SSH ključ. U
`authorized_keys` jednog sistemskog korisnika taj ključ ima red sa prinudnom
komandom `shfd rsync-gate`, koja propušta samo dva prenosa: upis u
`tmp/<upload_id>` sopstvenog otvorenog uploada i čitanje iz `storage/`.
Šta se prenosi i dalje odlučuje kontrolni API; rsync je samo prenos.

## 1. Paketi

Na serveru: `openssh-server` i `rsync`, a za sličice PDF-ova
`poppler-utils` (`pdftoppm`; bez njega `shfd thumbs` pravi samo sličice
slika, [odeljak 7](#7-sličice-shfd-thumbs)).

```sh
apt install openssh-server rsync poppler-utils   # Debian/Ubuntu
```

Na klijentu: `ssh` i **rsync 3.2.4 ili noviji**. Starije verzije drugačije
pakuju imena fajlova u komandu koju šalju serveru. `openrsync`, koji macOS
isporučuje kao `/usr/bin/rsync` (predstavlja se kao 2.6.9), ne radi:

```sh
brew install rsync
echo 'eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"' >> ~/.zprofile
source ~/.zprofile                          # Homebrew ispred /usr/bin (Apple čip i Intel)
rsync --version | head -1                   # rsync  version 3.x.y
```

`shf` proverava verziju pri pokretanju i, ako ne odgovara, kaže koji je
rsync našao (putanju) i šta treba instalirati. `SHF_RSYNC` i `SHF_SSH`
(apsolutne putanje) imaju prednost nad `PATH`-om.

Pozadinski klijent (`shf service install`) ne vidi `PATH` shell-a. Zato
`shf service install` pamti putanje alata: upisuje u systemd jedinicu ili
launchd agent apsolutne putanje `rsync`-a i `ssh`-a koje je našao
(`SHF_RSYNC`, `SHF_SSH`) i `PATH` sa `/opt/homebrew/bin` i `/usr/local/bin`
ispred sistemskih putanja. Star rsync ili `openrsync` odbija i ne instalira
ništa. Posle zamene ili nadogradnje rsync-a ponovo pokrenite
`shf service install`.

## 2. Sistemski korisnik

`shfd` radi pod jednim sistemskim korisnikom. sshd pod tim istim korisnikom
pokreće `rsync-gate` za svakog člana.

```sh
useradd --system --create-home --home-dir /var/lib/shfd --shell /bin/sh shfd
install -d -o shfd -g shfd -m 700 /var/lib/shfd/data /var/lib/shfd/db /var/lib/shfd/.ssh
install -o shfd -g shfd -m 600 /dev/null /var/lib/shfd/.ssh/authorized_keys
install -m 755 bin/shfd /usr/local/bin/shfd
```

- Korisnik mora imati **pravu ljusku** (`/bin/sh`), ne `nologin`: sshd prinudnu
  komandu pokreće kao `<ljuska> -c "<komanda>"`. Šta god ljuska ispiše pri
  pokretanju (`.profile`, `.bashrc`) kvari rsync protokol, zato tom korisniku
  ne dodajte takve fajlove.
- Data-root (`/var/lib/shfd/data`), db-root (`/var/lib/shfd/db`) i
  `authorized_keys` pripadaju korisniku `shfd`.

## 3. Konfiguracija `shfd`

Putanja do `authorized_keys` se zadaje isto kao data-root: zastavicom ili
promenljivom okruženja. Važi za `serve`, `member …` i `keys sync`.
`--data-root` i `--db-root` važe za svaku komandu (`serve`, `share`,
`member`, `keys`, `trash`, `verify`, `stats`, `db`, `thumbs`, `rsync-gate`).

| Zastavica | Promenljiva | Značenje |
|---|---|---|
| `--data-root` | `SHFD_DATA_ROOT` | podaci: `<share-id>/storage/`, `trash/`, `tmp/`, `thumbnails/`, `service.log` |
| `--db-root` | `SHFD_DB_ROOT` | SQLite baze: `registry.db`, `<share-id>/service.db` (sa `-wal` i `-shm`), kao i `serve.lock` i `admin.sock`; podrazumevano isto što i data-root |
| `--authorized-keys` | `SHFD_AUTHORIZED_KEYS` | `authorized_keys` korisnika `shfd`; bez nje se SSH ključevi ne mogu registrovati |
| `--shfd-binary` | `SHFD_BINARY` | apsolutna putanja do `shfd` koja se upisuje u prinudnu komandu; podrazumevano je to sam izvršni fajl |

### Baze na lokalnom disku

`storage/` može biti na NFS-u (ili drugom mrežnom disku), **baze ne treba**.
SQLite izričito ne preporučuje WAL i POSIX zaključavanje na mrežnom
fajl-sistemu: kad NFS klijent prestane da završava I/O, nit koja otpušta
SQLite lock ostaje u kernelu (`D` stanje, `nfs_iocounter_wait ← do_unlk ←
fcntl_setlk`), a sa njom i svi zahtevi tog foldera. Zato:

- `SHFD_DB_ROOT` (ili `--db-root`) postavite na direktorijum na lokalnom
  disku, npr. `/var/lib/shfd/db`. Tamo idu `registry.db` i
  `<db-root>/<share-id>/service.db`. `storage/`, `tmp/`, `trash/` i
  `service.log` ostaju u data-root-u.
- `serve.lock` (jedan `shfd serve` po bazama) i `admin.sock` su takođe u
  db-root-u: `flock` na NFS-u postaje POSIX lock na NFS serveru, iste vrste
  kao onaj koji je zaglavio `shfd`, a unix socket nema šta da traži na
  mrežnom disku. Dva `shfd serve`-a na istim bazama i dalje ne mogu da rade
  istovremeno.
- `shfd serve` pri startu proverava fajl-sistem db-root-a (Linux: `statfs`
  magic za NFS, CIFS/SMB, Ceph, 9p, i FUSE `sshfs`/`rclone`/…; macOS:
  `f_fstypename` i `MNT_LOCAL`). Ako je mrežni, upisuje `level=WARN` u log
  sa preporukom za `--db-root`; server ipak radi.
- Postojeće baze se premeštaju komandom `shfd db move` dok server ne radi:
  [admin.md, Premeštanje baza](admin.md#premeštanje-baza-shfd-db-move).
- Kad je db-root odvojen, prinudna komanda u `authorized_keys` nosi i
  `--db-root` (sshd ne prosleđuje `SHFD_DB_ROOT` `rsync-gate`-u). Posle
  promene db-root-a pokrenite `shfd keys sync`.
- Ako db-root nema `registry.db`, a data-root ima, `shfd` odbija da radi (da
  ne bi krenuo sa praznom listom foldera) i kaže da prvo treba `shfd db
  move`.

`GET /healthz` pored `registry.db` proverava i pisca svakog otvorenog
foldera: da li baza odgovara na čitanje i da li se lock pisca oslobodi u roku
od 2 s. Ako ne, vraća 503 sa `"ok":false` i `"error":"share <id> stuck"`
(`"share <id> unavailable"` kad baza vrati grešku). Dok radi `shfd verify`,
koji drži lock dugo, proverava se samo baza.

Primer systemd jedinice `/etc/systemd/system/shfd.service`:

```ini
[Service]
User=shfd
Environment=SHFD_DATA_ROOT=/var/lib/shfd/data
Environment=SHFD_DB_ROOT=/var/lib/shfd/db
Environment=SHFD_AUTHORIZED_KEYS=/var/lib/shfd/.ssh/authorized_keys
Environment=SHFD_BINARY=/usr/local/bin/shfd
# paketi za preuzimanje (make release), docs/install.md; može i bez
Environment=SHFD_DOWNLOADS=/var/lib/shfd/downloads
# proxy-ji čiji se X-Forwarded-For veruje (prijava lozinkom); vidi niže
#Environment=SHFD_TRUSTED_PROXIES=127.0.0.1
# mejl za zaboravljenu lozinku (Brevo), docs/deploy.md#mejl-brevo; može i bez
#Environment=SHFD_BREVO_KEY_FILE=/etc/shfd/brevo.key
#Environment=SHFD_PUBLIC_URL=https://shf.example.org
#Environment=SHFD_GUI_BASE=/gui
ExecStart=/usr/local/bin/shfd serve --listen 127.0.0.1:8443
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`/var/lib/shfd/data` može biti NFS mount (npr. `/disk2/shared-storage`), a
`/var/lib/shfd/db` mora biti na lokalnom disku.

Admin komande pokrenite kao isti korisnik i sa istim okruženjem, npr.
`sudo -u shfd env SHFD_DATA_ROOT=… SHFD_DB_ROOT=… SHFD_AUTHORIZED_KEYS=… shfd member revoke <share-id> bob`.

### Reverse proxy (vhost)

`shfd serve` sluša na `127.0.0.1:8443` bez TLS-a; ispred je nginx. Klijent
koristi `/v1/…`, uključujući WebSocket `/v1/shares/{id}/ws` (nginx mora da
prosledi `Upgrade`/`Connection` i drži vezu dugo otvorenom), a bez tokena
idu `/healthz`, `/downloads/…` i `/v1/downloads`.

- **Bez web GUI-ja** ili sa GUI-jem pod `/gui`: `location /` → `127.0.0.1:8443`
  (sa WebSocket zaglavljima), a za GUI se doda
  `gui/deploy/nginx-gui.conf`.
- **Sa web GUI-jem na korenu hosta** (preporučeno, `https://<host>/` je GUI):
  ceo vhost po `gui/deploy/nginx-gui-root.conf`: `location /v1/`,
  `location = /healthz` i `location /downloads/` idu na shfd (8443), sve
  ostalo na shfd-gui (8444), a `/gui/…` dobija `301` na `/…`. shfd-ov
  `GET /` se tada spolja ne vidi; za proveru služi `/healthz`.
  Vidi [gui.md](gui.md#base-path-koren-ili-gui).

### Adresa klijenta (X-Forwarded-For)

Prijava lozinkom (`POST /v1/auth/login`, [admin.md](admin.md#nalozi))
ograničava pokušaje i po adresi klijenta i upisuje je u `auth_log`. Iza
reverse proxy-ja shfd vidi samo adresu proxy-ja, pa adresu klijenta čita iz
`X-Forwarded-For`, ali **samo** kad veza dolazi od pouzdanog proxy-ja:

```sh
shfd serve --trusted-proxy 127.0.0.1,<adresa spoljnog proxy-ja>   # ili SHFD_TRUSTED_PROXIES
```

Lista su adrese ili CIDR opsezi, odvojeni zarezom; podrazumevano
`127.0.0.1` (nginx na istoj mašini). Od desnog kraja zaglavlja shfd
preskače pouzdane hop-ove, a prvi sledeći je klijent; sve levo od njega
(što je klijent mogao sam da pošalje) se ignoriše. Zahtev koji ne dolazi od
pouzdanog proxy-ja ostaje na svojoj adresi, šta god pisalo u zaglavlju.

**Dva nginx-a** (spoljni, npr. na hipervizoru, koji prosleđuje na nginx na
VM-u, koji prosleđuje na shfd na `127.0.0.1:8443`):

1. **Oba** moraju da dodaju adresu u zaglavlje, u svakoj `location` koja
   ide ka shfd-u (`/v1/`):
   ```nginx
   proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
   ```
   `gui/deploy/nginx-gui-root.conf` i `nginx-gui.conf` to već imaju za nginx
   na VM-u; spoljni nginx treba proveriti i podesiti pri deploy-u. Ako
   spoljni ne šalje zaglavlje, svi klijenti izgledaju kao on, pa pet
   pogrešnih lozinki bilo koga zaključava prijavu svima.
2. shfd mora da veruje i **spoljnom** proxy-ju: nginx na VM-u dodaje adresu
   spoljnog (`klijent, <spoljni>`), pa bez nje u `--trusted-proxy` shfd bi
   za klijenta uzeo spoljni proxy:
   ```ini
   Environment=SHFD_TRUSTED_PROXIES=127.0.0.1,<adresa spoljnog nginx-a viđena sa VM-a>
   ```

Provera: posle jedne pogrešne prijave poslednji red `auth_log` treba da
ima adresu vašeg računara, a ne proxy-ja:

```sh
sqlite3 "$SHFD_DB_ROOT/registry.db" 'SELECT * FROM auth_log ORDER BY rowid DESC LIMIT 1'
```

## 4. `sshd`

Ako je `authorized_keys` na podrazumevanom mestu (`~shfd/.ssh/authorized_keys`),
sshd ne treba posebno podešavati. Preporučeno ograničenje, na kraju
`/etc/ssh/sshd_config` (ili u `/etc/ssh/sshd_config.d/shfd.conf`):

```
Match User shfd
    AuthorizedKeysFile /var/lib/shfd/.ssh/authorized_keys
    AuthenticationMethods publickey
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    DisableForwarding yes
    PermitTTY no
    PermitUserRC no
    PermitTunnel no
```

Ako je `authorized_keys` negde drugde, tu putanju dajte i sshd-u
(`AuthorizedKeysFile`) i `shfd`-u (`SHFD_AUTHORIZED_KEYS`). Sa uključenim
`StrictModes` (podrazumevano) taj fajl i svi direktorijumi iznad njega ne
smeju biti upisivi drugima. Posle izmene: `sshd -t && systemctl reload ssh`.

Port 22 (ili onaj koji sshd sluša) mora biti dostupan klijentima.

Klijent pri `shf join` od servera dobija gde da se poveže za rsync: korisnika,
host i port. To se zadaje pri pokretanju servera:

```sh
shfd serve … --ssh-endpoint shfd@shf.example.org:22   # ili SHFD_SSH_ENDPOINT
```

Bez toga klijent uzima host iz URL-a API-ja, port 22 i korisnika `shfd`;
`shf join … --ssh korisnik@host:port` sve to zamenjuje na strani klijenta.

## 5. Šta `shfd` upisuje u `authorized_keys`

`shfd` održava samo svoj blok između dva komentara. Redovi iznad i ispod
bloka, npr. adminov sopstveni ključ, ostaju netaknuti.

```
# >>> shfd managed block: do not edit by hand, `shfd keys sync` rewrites it >>>
command="/usr/local/bin/shfd rsync-gate --data-root /var/lib/shfd/data --share 690a2702-… --member 2",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc ssh-ed25519 AAAA… shfd:bob@690a2702-…
command="/usr/local/bin/shfd rsync-gate --data-root /var/lib/shfd/data --share 690a2702-… --member 3 --device 7",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc ssh-ed25519 AAAA… shfd:marko/macbook@690a2702-…
command="/usr/local/bin/shfd rsync-gate --data-root /var/lib/shfd/data --share 690a2702-… --member 3 --device 9",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc ssh-ed25519 AAAA… shfd:marko/linux-mare@690a2702-…
# <<< shfd managed block <<<
```

Postoje dve vrste redova:

- **red člana** (legacy kredencijal): jedan po (folder, član), za ključ
  registrovan tokenom člana; komentar `shfd:<član>@<share-id>`;
- **red uređaja**: jedan po (folder, član, uređaj), za ključ koji je uređaj
  naloga registrovao svojim **folder-tokenom** (vidi niže). Prinudna komanda
  ima i `--device <id>` (`devices.id` iz `registry.db`), a komentar je
  `shfd:<član>/<ime-uređaja>@<share-id>`. U komentaru ime uređaja ima samo
  slova, cifre, `.`, `_` i `-` (ostalo postaje `-`), najviše 32 znaka.

Uređaji istog člana imaju svako svoj red i svoj ključ, pa se ne otimaju
(Mac i Linux kao `marko` rade istovremeno).

U redu je i `--data-root`: sshd ne prosleđuje okruženje prinudnoj komandi, pa
`rsync-gate` drugačije ne bi znao gde su podaci. Isti ključ ne može imati dva
reda (sshd bi uvek uzeo prvi), pa se ključ koji već postoji u fajlu odbija
(`key_in_use`), bilo da je na redu člana, drugog uređaja ili van bloka.

- **Registracija ključa**, kao član i uz njegov token:
  ```sh
  curl -X PUT https://shf.example.org/v1/shares/<share-id>/ssh-key \
       -H "Authorization: Bearer $TOKEN" \
       -d "{\"public_key\": \"$(cat id_ed25519.pub)\"}"
  ```
  Sa tokenom člana ključ se upisuje u `members.ssh_pubkey` i u red člana; sa
  folder-tokenom uređaja u `member_devices.ssh_pubkey` i u red tog uređaja.
  Novi ključ zamenjuje stari istog kredencijala. Odgovor sadrži sačuvani
  ključ, njegov SHA256 otisak i `ssh` (korisnik, host i port iz
  `--ssh-endpoint`). `shf join` ovo radi sam.

  **Folder-token uređaja**: uređaj naloga (device token `shd_…`, vidi
  [admin.md](admin.md#kredencijali-uređaja-za-folder)) traži
  `POST /v1/shares/<share-id>/devices` i dobija svoj token za taj folder
  (`shf_…`, isti oblik kao token člana), pa njime registruje svoj ključ:
  ```sh
  FT=$(curl -s -X POST -H "Authorization: Bearer $DEVICE_TOKEN" \
       https://shf.example.org/v1/shares/<share-id>/devices | jq -r .token)
  curl -X PUT https://shf.example.org/v1/shares/<share-id>/ssh-key \
       -H "Authorization: Bearer $FT" -d "{\"public_key\": \"$(cat id_ed25519.pub)\"}"
  ```
  Ključ uređaja ne zamenjuje ničiji drugi, pa odgovor nikad nema `replaced`.
  Ponovni `POST …/devices` izdaje nov token, a stari token i ključ tog
  uređaja u folderu prestaju da važe (klijent registruje ključ ponovo).

  **Jedan član = jedan uređaj** važi samo za token člana (legacy): član ima
  jedan sopstveni ključ, pa kad isti član
  registruje ključ sa drugog uređaja, prvi uređaj više ne može da sinhronizuje
  (`Permission denied (publickey)`). Kad novi ključ zamenjuje **drugačiji**
  postojeći, odgovor ima i `"replaced": {"fingerprint": "SHA256:…"}` (otisak
  zamenjenog ključa), u `service.log` foldera ide red
  `key-replaced member=marko old=SHA256:… new=SHA256:…`, a `shf join`
  upozorava korisnika. Isti ključ ponovo (ponovljen `join` posle prekida) nije
  zamena. Zamena se ne odbija; za drugi uređaj ili osobu napravite novog člana
  (`shfd member add`) ili, bolje, nalog (`shfd user add`), čiji uređaji
  dobijaju svoje ključeve. Trenutni ključ svakog člana pokazuje kolona
  `SSH KEY` u `shfd member list` (vidi [admin.md](admin.md#jedan-član--jedan-uređaj)).
  Upozorenje `key-replaced` postoji samo za ključ člana.
- **Opoziv**:
  - `shfd member revoke <share-id> <username>` briše sve redove člana (i
    njegov i redove njegovih uređaja);
  - opoziv jednog uređaja u folderu (`DELETE
    /v1/shares/<id>/admin/members/<član>/devices/<id>`) briše red tog
    uređaja;
  - odjava ili opoziv uređaja naloga (`POST /v1/auth/logout`,
    `DELETE /v1/me/devices/<id>`) briše njegove redove u svim folderima;
  - `shfd user disable <username>` briše redove svih uređaja naloga (komandi
    dajte `--authorized-keys` ili `SHFD_AUTHORIZED_KEYS`).

  `rsync-gate` odbija opozvanog člana i ako red iz nekog razloga još postoji,
  i na SSH vezi koja je otvorena pre opoziva (vidi niže): član se proverava
  pri svakom prenosu, ne samo pri prijavi.
- **`rsync-gate --device D`**: za red uređaja `rsync-gate` pri **svakom**
  prenosu proverava i da su red uređaja u `member_devices`, uređaj u
  `registry.devices` (nije odjavljen ni istekao) i nalog (`users`, nije
  ugašen) aktivni, kao i član. Ako nije, prenos se odbija sa
  ```
  shfd rsync-gate: denied (device_revoked): device 7 is revoked in this folder
  ```
  (razlog može biti i `member marko is revoked`, `device 7: account
  disabled`, `device 7: device signed out`, …), a u `service.log` foldera
  ide red sa `device="macbook#7"` i `outcome=denied`. Klijent to tumači kao
  odbijen ključ, isto kao `Permission denied (publickey)`.
- **Popravka**: `shfd keys sync` ponovo ispiše ceo blok iz baza svih foldera:
  redove aktivnih članova koji imaju svoj ključ i redove uređaja čiji su
  kredencijal, član, uređaj i nalog aktivni i koji imaju ključ. Ispisuje
  koliko je kojih (`2 member key(s), 3 device key(s)`). Korisno posle
  vraćanja iz rezervne kopije ili ručne izmene.

## 6. Provera

```sh
# shfd radi i na kojoj je verziji (bez tokena; isto daje i GET /, osim kad
# je shfd-gui na korenu hosta)
curl https://shf.example.org/healthz
# {"service":"shfd","version":"v1.2.3","ok":true}
# 503 i {"service":"shfd","version":"v1.2.3","ok":false,"error":"registry unavailable"}
# kada registry.db ne odgovori za 2 s

# komanda koja nije rsync se odbija
ssh -i id_ed25519 shfd@shf.example.org id
# shfd rsync-gate: denied (not_rsync): only rsync is allowed on this key

# svaki poziv ostavlja red u service.log foldera
tail /var/lib/shfd/data/<share-id>/service.log
# 2026-11-23T15:02:11.5Z transfer member=bob dir=upload path="tmp/3f…" outcome=ok
# 2026-11-23T15:02:12.1Z transfer member=bob dir=download path="storage/docs/a.txt" outcome=ok
# 2026-11-23T15:03:40.0Z transfer member=bob dir=- path="" outcome=denied detail="not_rsync: only rsync is allowed on this key; command id"
# 2026-11-24T09:11:02.4Z transfer member=marko device="macbook#7" dir=download path="storage/a.txt" outcome=ok
# 2026-11-24T09:20:15.0Z transfer member=marko device="macbook#7" dir=- path="" outcome=denied detail="device_revoked: device 7 is revoked in this folder"
```

Ključ servera klijent pamti pri prvom povezivanju (to radi već `shf join`) u
svom direktorijumu stanja (`known_hosts` pored `state.db`), a ne u
korisnikovom `~/.ssh/known_hosts`.
Ako se kasnije promeni, prenos se odbija. Otisak ključa koji članovi mogu da
uporede: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.

Klijent drži jednu SSH vezu za sve prenose (OpenSSH multiplexing,
`ControlMaster=auto`, `ControlPersist=60s`): prvi prenos otvara vezu, a svaki
sledeći je samo nova sesija na njoj, bez novog key exchange-a i
autentikacije. `sshd` prinudnu komandu primenjuje na svaku sesiju, pa svaki
prenos i dalje prolazi kroz `rsync-gate`. Veza se zatvara 60 s posle
poslednjeg prenosa, pri gašenju `shf daemon`-a i kada server odbije člana.
Soket veze je u direktorijumu stanja (`ssh-<hash>`), ili, kada je ta putanja
preduga za unix soket, u `$XDG_RUNTIME_DIR/shf/` odnosno `/tmp/shf-<uid>/`
(direktorijum samo za korisnika). Na serveru to znači po jedan `sshd` proces
po aktivnom klijentu umesto po prenosu; `MaxSessions` (podrazumevano 10) je
dovoljan, jer klijent prenosi fajl po fajl.

## Šta `rsync-gate` propušta

| Smer | Komanda (od klijentovog rsync-a) | Uslov |
|---|---|---|
| upload | `rsync --server <opcije> . tmp/<upload_id>` | `upload_id` je otvoren upload istog člana; `--partial` za nastavljanje |
| download | `rsync --server --sender <opcije> . storage/<putanja>` | običan fajl unutar `storage/` posle razrešavanja `..` i symlinkova |

Ništa drugo: ni `thumbnails/`, ni `trash/`, ni baze (ni preko `..` ili
symlinka iz `storage/`).

Dozvoljene opcije: kratke `v q t z c I W`, rsync-ov sopstveni niz mogućnosti
iza `-e` (`-e.LsfxCIvu`), `--partial` (samo upload), `--inplace` (samo
download), `--stats`, `--timeout=N`, `--bwlimit=N`, `--compress-level=N`.
Sve ostalo se odbija, posebno brisanje, `--remove-source-files`,
`--partial-dir`/`--temp-dir`/`--backup-dir` (upis van `tmp/<upload_id>`),
`--files-from` i `-s` (koji bi argumente sakrio od provere). Komanda se deli
na reči bez ljuske, a rsync se pokreće direktno, sa apsolutnim putanjama koje
je izračunao `rsync-gate`.

## 7. Sličice: `shfd thumbs`

Sličice slika i PDF-ova za GUI pravi poseban, dugotrajan proces istog
binarnog fajla ([admin.md](admin.md#thumbnails)). Radi pod istim korisnikom
i sa istim okruženjem kao `shfd.service`, a treba mu pokrenut `shfd serve`:
poslove dobija preko `<db-root>/admin.sock`, SQLite ne otvara, čita
`storage/` i piše samo u `<share-id>/thumbnails/`. Za PDF-ove treba
`pdftoppm` (`apt install poppler-utils`); bez njega servis to jednom upiše
kao WARN i pravi samo sličice slika.

`/etc/systemd/system/shfd-thumbs.service`:

```ini
[Unit]
Description=shfd thumbnails
After=shfd.service

[Service]
User=shfd
Environment=SHFD_DATA_ROOT=/var/lib/shfd/data
Environment=SHFD_DB_ROOT=/var/lib/shfd/db
# Environment=SHFD_PDFTOPPM=/usr/bin/pdftoppm   # ako nije u PATH-u
ExecStart=/usr/local/bin/shfd thumbs --workers 1
Restart=on-failure
Nice=10
MemoryMax=1G

[Install]
WantedBy=multi-user.target
```

```sh
systemctl daemon-reload
systemctl enable --now shfd-thumbs
journalctl -u shfd-thumbs -f
# level=INFO msg="shfd thumbs starting" … kinds=image,pdf pdftoppm=/usr/bin/pdftoppm
# level=INFO msg="connected to shfd serve" pending=…
# level=INFO msg=thumbnail share=… hash=… kind=image took=41ms outcome=ready
```

- `Nice=10` i `MemoryMax=1G`: dekodiranje velike slike (do 64 megapiksela)
  troši CPU i stotine MiB; ako ga kernel ubije, systemd ga podiže ponovo, a
  fajl na kome je pao dobija `.fail` („crashed while generating“) i ne
  pokušava se ponovo. `shfd serve` to ne oseti.
- Prvo popunjavanje velikog foldera može i ručno, pre jedinice:
  `sudo -u shfd env SHFD_DATA_ROOT=… SHFD_DB_ROOT=… shfd thumbs --once --workers 2`.
- Ako `shfd serve` ne radi, servis čeka i pokušava ponovo na 2 s (jedan red
  u logu kad server nestane i jedan kad se vrati).
- Proveru da radi daje i GUI (sličice se pojavljuju), a bez njega:
  `ls /var/lib/shfd/data/<share-id>/thumbnails/*/ | head` i
  `curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "https://<host>/v1/shares/<share-id>/admin/files?dir=" | grep -o '"thumb":"[a-z]*"' | sort | uniq -c`.
