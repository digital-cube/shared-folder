# Instalacija `shf` (i `shfd`)

Dva puta: **gotov paket** (preuzmeš, raspakuješ, instaliraš; Go ne treba) ili
**build iz izvornog koda**. Šta posle instalacije (`shf join`, `shf service
install`) je u [README.md](../README.md#klijent) i na strani Instalacija u GUI-ju.

## Šta treba na svakom uređaju (oba puta)

`shf` prenosi fajlove sistemskim `rsync`-om preko `ssh`-a:

- **rsync 3.2.4 ili noviji** (`rsync --version | head -1`)
- **OpenSSH klijent** (`ssh`, `ssh-keygen`)

| OS | Komanda |
|---|---|
| Debian / Ubuntu | `sudo apt install rsync openssh-client` (Debian 11 ima rsync 3.2.3, prestar: Debian 12+ ili Ubuntu 22.04+) |
| Fedora / RHEL | `sudo dnf install rsync openssh-clients` |
| macOS | [Homebrew](https://brew.sh), pa `brew install rsync`; `/usr/bin/rsync` je Apple-ov `openrsync` i **ne radi**. Homebrew mora biti u `PATH`-u ispred `/usr/bin`: `eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"` u `~/.zprofile` (ista linija radi na Apple čipu, `/opt/homebrew`, i na Intel-u, `/usr/local`). `ssh` je već tu. |
| Windows | rsync i ssh iz MSYS2 (`pacman -S rsync openssh`) u `PATH`-u, ili apsolutne putanje u `SHF_RSYNC` i `SHF_SSH` |

## 1. Gotov paket (Linux x86-64, macOS Intel, macOS Apple Silicon)

Paketi se zovu `shared-folder-<verzija>-<os>-<arch>.tar.gz`, uz njih ide
`SHA256SUMS`. U svakom su `shf` (klijent), `shfd` (server), ovo uputstvo
(`INSTALL.md`) i `README.md`.

| Uređaj | Paket | Kako proveriti |
|---|---|---|
| Linux, x86-64 | `…-linux-amd64.tar.gz` | `uname -m` → `x86_64` |
| Mac sa Apple čipom (M1, M2, …) | `…-darwin-arm64.tar.gz` | `uname -m` → `arm64` |
| Mac sa Intel procesorom | `…-darwin-amd64.tar.gz` | `uname -m` → `x86_64` |

Za Windows i ostale platforme nema paketa: build iz izvornog koda (dole).

### Linux

```sh
cd ~/Downloads
grep linux-amd64 SHA256SUMS | sha256sum -c      # OK
tar -xzf shared-folder-<verzija>-linux-amd64.tar.gz
sudo install -m 755 shared-folder-<verzija>-linux-amd64/shf /usr/local/bin/shf
shf version
```

### macOS

```sh
uname -m                                         # arm64 ili x86_64: izaberi paket
cd ~/Downloads
grep darwin-arm64 SHA256SUMS | shasum -a 256 -c  # OK
tar -xzf shared-folder-<verzija>-darwin-arm64.tar.gz
sudo install -d -m 755 /usr/local/bin            # na Apple Silicon-u ga možda nema
sudo install -m 755 shared-folder-<verzija>-darwin-arm64/shf /usr/local/bin/shf

# skini karantin (obavezno, vidi ispod)
sudo xattr -d com.apple.quarantine "$(command -v shf)"
xattr -l "$(command -v shf)"                     # ne sme da ispiše com.apple.quarantine

shf version
```

**Karantin na macOS-u.** Fajl preuzet browser-om (ili iz priloga tiketa,
mejla, Slack-a) macOS označava atributom `com.apple.quarantine`, a raspakivanje
i `install` taj atribut prenose na `shf`. Paketi nisu potpisani Apple-ovim
sertifikatom ni notarizovani, pa Gatekeeper takav fajl ne pušta: u terminalu
`shf` biva odmah ubijen (`killed`) ili se pojavi prozor „“shf” cannot be opened
because Apple cannot check it for malicious software“, a launchd agent
(`shf service install`) tiho ne radi. Zato posle **svake** instalacije ili
zamene binarnog fajla:

```sh
sudo xattr -d com.apple.quarantine "$(command -v shf)"
xattr -l "$(command -v shf)"        # ne sme da ispiše com.apple.quarantine
```

- `No such xattr: com.apple.quarantine` znači da atributa nema; to je u redu
  (npr. paket preuzet sa `curl`-om nije u karantinu).
- `sudo` treba jer je fajl u `/usr/local/bin` vlasništvo root-a.
- Ako atribut skineš pre instalacije, može i za ceo raspakovan direktorijum:
  `xattr -dr com.apple.quarantine shared-folder-<verzija>-darwin-arm64`.
- Druga mogućnost: pokreni `shf` jednom, pa *System Settings → Privacy &
  Security → Open Anyway*. `xattr` je brži i radi i bez prozora.

Posle zamene binarnog fajla (nova verzija) ponovo: karantin, pa
`shf service install`.

### Server (`shfd`)

`shfd` iz paketa `linux-amd64` ide na server po
[docs/server-ssh.md](server-ssh.md) (`/opt/shfd/versions/<verzija>/shfd`,
symlink `/opt/shfd/current`).

## 2. Build iz izvornog koda

### Šta treba

| Alat | Za šta | Obavezno |
|---|---|---|
| **Go 1.27+** (verzija iz `go.mod`) | prevođenje `shf` i `shfd` | da |
| **make** (GNU make; i 3.81 na macOS-u je dovoljan) | `make build`, `make check`, `make release` | da (na Windows-u može i bez, vidi dole) |
| **git** | kloniranje; verzija u binarnom fajlu (`git describe`) | da |
| rsync 3.2.4+, `ssh`, `ssh-keygen`, `sshd` | integracioni testovi i `make e2e` (bez njih se preskaču) | za testove |
| Node 22+ i npm | `make gui` (shfd-gui, [docs/gui.md](gui.md)) | samo za GUI |
| golangci-lint | `make lint` ga pokreće ako postoji | ne |

C kompajler ne treba: sve je čist Go (SQLite je `modernc.org/sqlite`), pa se
za sve platforme prevodi sa jedne mašine.

**Go verzija.** Go 1.21 ili noviji sam preuzima Go 1.27 kad ga `go.mod`
traži (`GOTOOLCHAIN=auto`, podrazumevano). Distribucioni paketi (Debian,
Ubuntu, Fedora) često postavljaju `GOTOOLCHAIN=local`, pa `make build` javi
`go.mod requires go >= 1.27`: tada `GOTOOLCHAIN=auto make build`, ili Go sa
go.dev kao ispod. `go version` pokazuje šta je instalirano.

### Debian / Ubuntu

```sh
sudo apt install make git rsync openssh-client openssh-server   # openssh-server samo za testove
# Go sa go.dev (apt-ov golang-go je obično stariji):
curl -fsSL https://go.dev/dl/go1.27.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile && . ~/.profile
# ili: sudo snap install go --classic
go version                       # go1.27.x
```

### Fedora / RHEL

```sh
sudo dnf install golang make git rsync openssh-clients openssh-server
go version                       # ako je starija od 1.27: GOTOOLCHAIN=auto, ili Go sa go.dev
```

### macOS

```sh
xcode-select --install           # Command Line Tools: make, git, clang
# Homebrew: https://brew.sh; u PATH ispred /usr/bin (Apple čip: /opt/homebrew, Intel: /usr/local)
echo 'eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"' >> ~/.zprofile
source ~/.zprofile
brew install go rsync
go version
```

### Windows

```powershell
winget install GoLang.Go Git.Git
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
go build -trimpath -o shf.exe ./cmd/shf     # bez make-a
```

`make` na Windows-u: MSYS2 (`pacman -S make`) ili `winget install
GnuWin32.Make`. rsync i ssh za rad: MSYS2, vidi gore.

### Build

```sh
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                                  # bin/shf i bin/shfd
sudo install -m 755 bin/shf /usr/local/bin/shf
make check                                  # lint + test + build (pre commita)
```

Na macOS-u binarni fajl preveden lokalno nije u karantinu; `xattr` korak
treba samo za preuzete pakete.

### Pravljenje paketa za preuzimanje (`make release`)

```sh
make release                                # dist/shared-folder-<verzija>-{linux-amd64,darwin-amd64,darwin-arm64}.tar.gz + dist/SHA256SUMS
make release PLATFORMS="linux/arm64 windows/amd64"   # druge platforme
make release VERSION=v0.1.0                 # verzija u imenu paketa i u `shf version`
```

Verzija je `git describe --tags --always --dirty` (kratak SHA dok nema
tagova). Paketi nisu potpisani za macOS (nema Apple Developer ID-a), pa na
Mac-u važi korak sa karantinom iznad.
