# Postavljanje nove verzije: `deploy/deploy.sh`

Jedna komanda sa mašine za build postavlja na server `shfd`, `shfd-gui` i
pakete za preuzimanje iz jednog commita, restartuje servise, proverava ih i,
ako provera ne prođe, sama vraća prethodne verzije.

```sh
make deploy TARGET=test                       # HEAD na metu deploy/targets/test.env
make deploy TARGET=test REF=main DEPLOY_FLAGS=--dry-run
deploy/deploy.sh test --ref 4ec569a --only gui --skip-tests
```

```
deploy/deploy.sh <target> [--ref <git-ref>] [--dry-run] [--skip-tests]
                          [--only shfd|gui|downloads[,…]]
```

| Opcija | Značenje |
|---|---|
| `<target>` | `deploy/targets/<target>.env`, ili putanja do `.env` fajla |
| `--ref` | commit koji se postavlja (podrazumevano `HEAD`); verzija je njegov kratki hash |
| `--dry-run` | ispisuje sve korake i komande; na serveru samo čita, ne build-uje ništa |
| `--skip-tests` | bez `go test ./...` i `make gui-test` pre build-a |
| `--only` | samo ovi delovi (lista sa zarezima ili ponovljena opcija): `shfd`, `gui`, `downloads` |

Prvo postavljanje (korisnik, systemd jedinice, nginx, sshd) ostaje ručno:
[server-ssh.md](server-ssh.md) i [gui.md](gui.md#instalacija-na-server).
Skripta radi samo na serveru na kome to već postoji.

## Zahtevi

Na mašini sa koje se postavlja:

- **Go 1.27+** (koliko traži `go.mod` commita), **Node 22+** sa `npm`-om (za
  `gui`), `make`, `git`, `tar`, `curl`, `ssh`. Drugi Go: `GO=/putanja/go`.
- SSH pristup serveru ključem (bez lozinke; `BatchMode=yes`), po potrebi preko
  jump hosta, kao korisnik sa `sudo` bez lozinke.

Na serveru: `bash`, `curl`, `tar`, GNU `find`, `sha256sum`, `systemctl`, i već
postavljeni `<prefix>/versions/<sha>` i `<prefix>/current` za `shfd` i
`shfd-gui`, jedinice `shfd`, `shfd-thumbs`, `shfd-gui` i direktorijum paketa
(`SHFD_DOWNLOADS`).

## Konfiguracija mete

`deploy/targets/<ime>.env`, po uzoru na `deploy/targets/example.env`. U
repou je samo primer: prave mete su u `.gitignore`. Fajl se učitava kao bash
(`IME=vrednost`); promenljive okruženja ne „cure“ u njega (izvezeni `SUDO` ili
`SSH_TARGET` iz shell-a se ne vide), a `DEPLOY_<IME>=…` u okruženju menja
vrednost iz fajla za jedno pokretanje (npr. `DEPLOY_CHECK_TIMEOUT=60`).

| Promenljiva | Podrazumevano | Značenje |
|---|---|---|
| `SSH_TARGET` | — (obavezno) | `korisnik@host`, ili `local` (ova mašina je „server“, za testove) |
| `SSH_JUMP` | — | jump host (`ssh -J`) |
| `SSH_OPTS` | — | dodatne opcije za `ssh` (`-i …`, `-p …`) |
| `PUBLIC_URL` | — (obavezno) | javni URL; tu se posle restarta proverava |
| `SHFD_PREFIX` | `/opt/shfd` | `versions/<sha>/shfd` i `current` |
| `GUI_PREFIX` | `/opt/shfd-gui` | `versions/<sha>/` (raspakovan `gui/dist`) i `current` |
| `DOWNLOADS_DIR` | `SHFD_DOWNLOADS` iz jedinice `shfd` | paketi `make release` i `SHA256SUMS` |
| `KEEP_VERSIONS` | `5` | koliko najnovijih verzija ostaje u `versions/` (najmanje 2) |
| `SHFD_SERVICES` | `shfd shfd-thumbs` | jedinice koje se restartuju za `shfd`, tim redom |
| `GUI_SERVICES` | `shfd-gui` | jedinice koje se restartuju za `gui` |
| `SHFD_LOCAL_URL` | `http://127.0.0.1:8443` | shfd, proverava se na serveru |
| `GUI_LOCAL_URL` | `http://127.0.0.1:8444` | shfd-gui, proverava se na serveru |
| `GUI_BASE` | `SHFD_GUI_BASE` iz jedinice `shfd-gui`, inače `/gui` | prazno = GUI na korenu hosta |
| `GUI_PUBLIC_URL` | `$PUBLIC_URL$GUI_BASE` | javni URL GUI-ja |
| `CHECK_TIMEOUT` | `30` | koliko sekundi se provere ponavljaju |
| `SERVER_PLATFORM` | `linux/amd64` | `GOOS/GOARCH` za `shfd` na serveru |
| `SUDO` | `sudo` | prazno: bez sudo-a (korisnik je vlasnik prefiksa) |
| `SYSTEMCTL` | `systemctl` | test ga zamenjuje lažnim |

Primer mete iza jump hosta, `deploy/targets/test.env` (adrese i hostovi
pravih servera ne idu u repo):

```sh
SSH_TARGET=shfd@10.0.0.3
SSH_JUMP=jump@bastion.example.org
PUBLIC_URL=https://shf.example.org
SHFD_PREFIX=/opt/shfd
GUI_PREFIX=/opt/shfd-gui
DOWNLOADS_DIR=/var/lib/shfd/downloads
KEEP_VERSIONS=5
SHFD_SERVICES="shfd shfd-thumbs"
GUI_SERVICES="shfd-gui"
```

## Šta skripta radi

1. **Lokalna provera**: alati, verzije Go-a i Node-a, ref → `<sha>`.
   Nesačuvane izmene u radnom stablu se samo pominju: build ih ne vidi.
2. **Provera servera** (samo čitanje, i u `--dry-run`): jedinice postoje
   (`systemctl cat`), `current` pokazuje na postojeći `versions/<x>`, direktorijum
   paketa postoji. Pamte se trenutni ciljevi `current` (to su „prethodne“
   verzije za povratak). Ako nešto od ovoga ne postoji, skripta to kaže i
   staje bez ikakve izmene (izlaz 1). Deo koji je već na `<sha>` se preskače;
   ako je sve već tu, samo se proverava da odgovara.
3. **Build** iz `git archive <sha>` u privremeni direktorijum, nikad iz radnog
   stabla: `go test ./...` i `make gui-test` (osim sa `--skip-tests`),
   `shfd` za `SERVER_PLATFORM` sa `-X …/internal/version.Version=<sha>`,
   `make gui VERSION=<sha>` (pa `tar` od `gui/dist`) i
   `make release VERSION=<sha>`. Proverava se da napravljeni `shfd version`
   kaže `<sha>` (kad je platforma ista kao lokalna).
4. **Slanje** jednim `tar`-om preko `ssh` u privremeni direktorijum na serveru.
5. **Instalacija** (još ništa nije uživo): `versions/<sha>.new` → `versions/<sha>`
   za oba prefiksa; paketi u direktorijum paketa pod skrivenim privremenim
   imenom pa `mv`, a `SHA256SUMS` na kraju. Do restarta `SHA256SUMS` sadrži i
   stare i nove pakete, da `shf update` klijenata koji još vide stari `shfd`
   i dalje radi.
6. **Zamena** `current`, atomično:
   `ln -sfn versions/<sha> current.new && mv -T current.new current`.
7. **Restart** `SHFD_SERVICES` (za `shfd`) i `GUI_SERVICES` (za `gui`), jedan
   po jedan.
8. **Provere**, svaka se ponavlja do `CHECK_TIMEOUT` sekundi:
   - na serveru: `SHFD_LOCAL_URL/healthz` (`"ok":true`, `"version":"<sha>"`) i
     `GUI_LOCAL_URL<GUI_BASE>/api/healthz` (isto, verzija GUI-ja);
   - javno, sa mašine za build: `PUBLIC_URL/healthz`,
     `GUI_PUBLIC_URL/api/healthz` i `PUBLIC_URL/v1/downloads`
     (`"enabled":true`, paketi verzije `<sha>` sa `sha256`).

   Deo koji se ne postavlja se ne proverava po verziji: `shfd` mora samo da
   bude zdrav, GUI se tada ne dira. (Stari `/gui/healthz` ne nosi verziju,
   zato se gleda `<base>/api/healthz`.)
9. **Čišćenje** posle uspeha: u `versions/` ostaje `KEEP_VERSIONS` najnovijih
   (po vremenu instalacije), a trenutna i prethodna uvek; ostaci `*.new` se
   brišu. U direktorijumu paketa ostaju samo `shared-folder-<sha>-*.tar.gz` i
   `SHA256SUMS` (samo njihove linije); fajlovi koji nisu paketi se ne diraju.
   Ako čišćenje ne uspe, postavljanje je ipak uspelo (upozorenje, izlaz 0).

Na kraju ide izveštaj: šta je bilo, šta je sada i šta je restartovano.

### Šta skripta ne radi

- ne briše podatke ni baze (`SHFD_DATA_ROOT`, `SHFD_DB_ROOT`): briše samo
  stare `versions/<x>`, stare pakete i svoj privremeni direktorijum;
- ne menja systemd jedinice, nginx ni sshd, i ne radi prvo postavljanje;
- ne radi migracije: `shfd serve` ih radi sam pri startu;
- nije CI i ne povlači nove verzije sama;
- nema zaključavanje: dva istovremena postavljanja na isti server nisu
  podržana.

## Povratak (automatski)

Ako posle izmene na serveru bilo šta ne uspe (instalacija, zamena, restart,
lokalna ili javna provera, ili prekid sa Ctrl-C), skripta:

1. vraća `current` oba prefiksa na zapamćene prethodne verzije (isto,
   atomično);
2. iz direktorijuma paketa briše pakete koje je ovo postavljanje dodalo i
   vraća stari `SHA256SUMS`;
3. restartuje iste jedinice i proverava da prethodne verzije odgovaraju;
4. izlazi sa **3** i izveštajem (razlog, gde je šta sada). `versions/<sha>`
   neuspele verzije ostaje na serveru za pregled; sledeće čišćenje ga briše.

Ako ni povratak ili provera posle njega ne uspe: izlaz **4**, i treba ručni
povratak (niže). Ostali izlazi: **0** uspeh (ili već postavljeno), **1**
greška pre bilo kakve izmene na serveru, **2** pogrešna upotreba ili
konfiguracija.

Svaka komanda koja na serveru nešto menja se ispisuje (`[server] $ …`), pa se
iz izlaza vidi tačno šta je urađeno.

## Ručni povratak

Na serveru (`ssh [-J <SSH_JUMP>] <SSH_TARGET>`):

```sh
ls -lt /opt/shfd/versions /opt/shfd-gui/versions     # koje verzije postoje
readlink /opt/shfd/current /opt/shfd-gui/current      # koja je trenutna

prev=<sha>                                            # verzija na koju se vraća
cd /opt/shfd     && sudo ln -sfn versions/$prev current.new && sudo mv -T current.new current
cd /opt/shfd-gui && sudo ln -sfn versions/$prev current.new && sudo mv -T current.new current
sudo systemctl restart shfd shfd-thumbs shfd-gui

curl -s 127.0.0.1:8443/healthz                        # "version":"<prev>"
curl -s 127.0.0.1:8444/gui/api/healthz                # ili /api/healthz kad je GUI na korenu
```

Paketi prethodne verzije posle uspešnog postavljanja više ne postoje (čišćenje
ih briše). Ako treba i njih vratiti, najlakše je ponovo postaviti tu verziju:
`deploy/deploy.sh <target> --ref <prev>` (`versions/<prev>` se tada zameni
novim build-om istog commita).

## Test

```sh
make deploy-test            # ili deploy/test/deploy_test.sh [REF]
```

Server je privremeni direktorijum na ovoj mašini (`opt/shfd`, `opt/shfd-gui`,
`downloads/`, `data/`), do koga se ide pravim `ssh`-om kroz privremeni `sshd`
na `127.0.0.1` (kao u `internal/sshdtest`; bez `sshd`-a sa `SSH_TARGET=local`,
`DEPLOY_TEST_TRANSPORT=local|ssh` bira). Jedinice su pravi `shfd serve`,
`shfd thumbs` i `shfd-gui` koje pokreće `deploy/test/fake-systemctl`.
Scenariji: `--dry-run` ne menja ništa; `shfd` nove verzije ne ustaje → povratak
(izlaz 3, `current` i paketi kao pre, stare verzije odgovaraju); uspešno
postavljanje (verzije, provere, čišćenje sa `KEEP_VERSIONS=2`: dve najnovije i
prethodna, samo novi paketi, podaci netaknuti); isti commit ponovo samo
proverava; pogrešna upotreba izlazi sa 2. REF se build-uje dva puta, pa traje
par minuta; `DEPLOY_TEST_RUN_TESTS=1` uključuje i `go test` i `make gui-test`,
`KEEP_TMP=1` ostavlja direktorijum.

`make lint` proverava skripte `shellcheck`-om kad je instaliran (inače
`make lint SHELLCHECK="uvx --from shellcheck-py shellcheck"`).
