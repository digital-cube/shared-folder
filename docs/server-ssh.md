# Postavljanje servera za rsync preko SSH-a

Fajlovi između klijenta `shf` i servera `shfd` idu rsync-om preko sistemskog
`sshd`-a (specifikacija, odeljak 5). Svaki član ima svoj SSH ključ. U
`authorized_keys` jednog sistemskog korisnika taj ključ ima red sa prinudnom
komandom `shfd rsync-gate`, koja propušta samo dva prenosa: upis u
`tmp/<upload_id>` sopstvenog otvorenog uploada i čitanje iz `storage/`.
Šta se prenosi i dalje odlučuje kontrolni API; rsync je samo prenos.

## 1. Paketi

Na serveru: `openssh-server` i `rsync`.

```sh
apt install openssh-server rsync          # Debian/Ubuntu
```

Na klijentu: `ssh` i **rsync 3.2.4 ili noviji**. Starije verzije drugačije
pakuju imena fajlova u komandu koju šalju serveru. `openrsync`, koji macOS
isporučuje, ne radi; instalirajte `brew install rsync`. `shf` proverava
verziju pri pokretanju i, ako ne odgovara, kaže šta treba instalirati.

## 2. Sistemski korisnik

`shfd` radi pod jednim sistemskim korisnikom. sshd pod tim istim korisnikom
pokreće `rsync-gate` za svakog člana.

```sh
useradd --system --create-home --home-dir /var/lib/shfd --shell /bin/sh shfd
install -d -o shfd -g shfd -m 700 /var/lib/shfd/data /var/lib/shfd/.ssh
install -o shfd -g shfd -m 600 /dev/null /var/lib/shfd/.ssh/authorized_keys
install -m 755 bin/shfd /usr/local/bin/shfd
```

- Korisnik mora imati **pravu ljusku** (`/bin/sh`), ne `nologin`: sshd prinudnu
  komandu pokreće kao `<ljuska> -c "<komanda>"`. Šta god ljuska ispiše pri
  pokretanju (`.profile`, `.bashrc`) kvari rsync protokol, zato tom korisniku
  ne dodajte takve fajlove.
- Data-root (`/var/lib/shfd/data`) i `authorized_keys` pripadaju korisniku `shfd`.

## 3. Konfiguracija `shfd`

Putanja do `authorized_keys` se zadaje isto kao data-root: zastavicom ili
promenljivom okruženja. Važi za `serve`, `member …` i `keys sync`.

| Zastavica | Promenljiva | Značenje |
|---|---|---|
| `--data-root` | `SHFD_DATA_ROOT` | podaci |
| `--authorized-keys` | `SHFD_AUTHORIZED_KEYS` | `authorized_keys` korisnika `shfd`; bez nje se SSH ključevi ne mogu registrovati |
| `--shfd-binary` | `SHFD_BINARY` | apsolutna putanja do `shfd` koja se upisuje u prinudnu komandu; podrazumevano je to sam izvršni fajl |

Primer systemd jedinice `/etc/systemd/system/shfd.service`:

```ini
[Service]
User=shfd
Environment=SHFD_DATA_ROOT=/var/lib/shfd/data
Environment=SHFD_AUTHORIZED_KEYS=/var/lib/shfd/.ssh/authorized_keys
Environment=SHFD_BINARY=/usr/local/bin/shfd
ExecStart=/usr/local/bin/shfd serve --listen 127.0.0.1:8443
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Admin komande pokrenite kao isti korisnik i sa istim okruženjem, npr.
`sudo -u shfd env SHFD_DATA_ROOT=… SHFD_AUTHORIZED_KEYS=… shfd member revoke <share-id> bob`.

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

## 5. Šta `shfd` upisuje u `authorized_keys`

`shfd` održava samo svoj blok između dva komentara. Redovi iznad i ispod
bloka, npr. adminov sopstveni ključ, ostaju netaknuti.

```
# >>> shfd managed block: do not edit by hand, `shfd keys sync` rewrites it >>>
command="/usr/local/bin/shfd rsync-gate --data-root /var/lib/shfd/data --share 690a2702-… --member 2",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc ssh-ed25519 AAAA… shfd:bob@690a2702-…
# <<< shfd managed block <<<
```

U redu je i `--data-root`: sshd ne prosleđuje okruženje prinudnoj komandi, pa
`rsync-gate` drugačije ne bi znao gde su podaci. Isti ključ ne može imati dva
reda (sshd bi uvek uzeo prvi), pa se ključ koji već postoji u fajlu odbija.

- **Registracija ključa**, kao član i uz njegov token:
  ```sh
  curl -X PUT https://shf.example.org/v1/shares/<share-id>/ssh-key \
       -H "Authorization: Bearer $TOKEN" \
       -d "{\"public_key\": \"$(cat id_ed25519.pub)\"}"
  ```
  Ključ se upisuje u `members.ssh_pubkey` i u blok. Novi ključ zamenjuje
  stari. Odgovor sadrži sačuvani ključ i njegov SHA256 otisak.
- **Opoziv**: `shfd member revoke <share-id> <username>` briše red. `rsync-gate`
  odbija opozvanog člana i ako red iz nekog razloga još postoji.
- **Popravka**: `shfd keys sync` ponovo ispiše ceo blok iz baza svih foldera
  (aktivni članovi koji imaju ključ). Korisno posle vraćanja iz rezervne kopije
  ili ručne izmene.

## 6. Provera

```sh
# komanda koja nije rsync se odbija
ssh -i id_ed25519 shfd@shf.example.org id
# shfd rsync-gate: denied (not_rsync): only rsync is allowed on this key

# svaki poziv ostavlja red u service.log foldera
tail /var/lib/shfd/data/<share-id>/service.log
# 2026-11-23T15:02:11.5Z transfer member=bob dir=upload path="tmp/3f…" outcome=ok
# 2026-11-23T15:02:12.1Z transfer member=bob dir=download path="storage/docs/a.txt" outcome=ok
# 2026-11-23T15:03:40.0Z transfer member=bob dir=- path="" outcome=denied detail="not_rsync: only rsync is allowed on this key; command id"
```

Ključ servera klijent pamti pri prvom povezivanju u svom direktorijumu stanja
(`known_hosts` pored `state.db`), a ne u korisnikovom `~/.ssh/known_hosts`.
Ako se kasnije promeni, prenos se odbija. Otisak ključa koji članovi mogu da
uporede: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.

## Šta `rsync-gate` propušta

| Smer | Komanda (od klijentovog rsync-a) | Uslov |
|---|---|---|
| upload | `rsync --server <opcije> . tmp/<upload_id>` | `upload_id` je otvoren upload istog člana; `--partial` za nastavljanje |
| download | `rsync --server --sender <opcije> . storage/<putanja>` | običan fajl unutar `storage/` posle razrešavanja `..` i symlinkova |

Dozvoljene opcije: kratke `v q t z c I W`, rsync-ov sopstveni niz mogućnosti
iza `-e` (`-e.LsfxCIvu`), `--partial` (samo upload), `--inplace` (samo
download), `--stats`, `--timeout=N`, `--bwlimit=N`, `--compress-level=N`.
Sve ostalo se odbija, posebno brisanje, `--remove-source-files`,
`--partial-dir`/`--temp-dir`/`--backup-dir` (upis van `tmp/<upload_id>`),
`--files-from` i `-s` (koji bi argumente sakrio od provere). Komanda se deli
na reči bez ljuske, a rsync se pokreće direktno, sa apsolutnim putanjama koje
je izračunao `rsync-gate`.
