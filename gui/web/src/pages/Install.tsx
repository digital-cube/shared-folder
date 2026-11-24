import { useEffect, useState, type ReactNode } from "react";
import { useAppMaybe } from "../App";
import { detectMacArch, dirOf, formatSize, getDownloads, pick, type Downloads, type MacArch, type Package } from "../downloads";
import { Download, Folder } from "../icons";
import { detectOs, type Os } from "../os";

function Code({ children }: { children: string }) {
  const [done, setDone] = useState(false);
  return (
    <pre>
      {children}
      <button
        className="copy"
        onClick={() =>
          void navigator.clipboard?.writeText(children.replace(/\s+#.*$/gm, "")).then(() => {
            setDone(true);
            setTimeout(() => setDone(false), 1200);
          })
        }
      >
        {done ? "Kopirano" : "Kopiraj"}
      </button>
    </pre>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <div className="step">
      <div className="num">{n}</div>
      <div>
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}

type How = "pkg" | "src";

// Ready-made packages from `make release` (docs/install.md); there is none for Windows. With
// the package this server offers (shfd serve --downloads) the commands use its exact name and
// check it against its SHA256SUMS line; without one they work on whatever is in ~/Downloads.
function pkgLinux(p?: Package): string {
  if (!p) {
    return `cd ~/Downloads                   # shared-folder-<verzija>-linux-amd64.tar.gz
tar -xzf shared-folder-*-linux-amd64.tar.gz
sudo install -m 755 shared-folder-*-linux-amd64/shf /usr/local/bin/shf
sudo apt install rsync openssh-client   # rsync 3.2.4+
shf version`;
  }
  return `cd ~/Downloads
${p.sha256 ? `echo "${p.sha256}  ${p.name}" | sha256sum -c   # ${p.name}: OK\n` : ""}tar -xzf ${p.name}
sudo install -m 755 ${dirOf(p)}/shf /usr/local/bin/shf
sudo apt install rsync openssh-client   # rsync 3.2.4+
shf version`;
}

const MAC_REST = `sudo xattr -d com.apple.quarantine "$(command -v shf)"   # skida karantin
xattr -l "$(command -v shf)"     # ne sme da ispiše com.apple.quarantine
echo 'eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"' >> ~/.zprofile
source ~/.zprofile               # Homebrew ispred /usr/bin; radi i na Apple čipu i na Intel-u
brew install rsync               # /usr/bin/rsync (openrsync) ne radi
rsync --version | head -1        # rsync  version 3.x.y (ne openrsync)
shf version`;

function pkgMac(arch: MacArch, p?: Package): string {
  if (!p) {
    return `cd ~/Downloads                   # …-darwin-arm64 (M1, M2…) ili …-darwin-amd64 (Intel)
tar -xzf shared-folder-*-darwin-$(uname -m | sed s/x86_64/amd64/).tar.gz
sudo install -d -m 755 /usr/local/bin
sudo install -m 755 shared-folder-*-darwin-*/shf /usr/local/bin/shf
${MAC_REST}`;
  }
  return `uname -m                         # ${arch === "arm64" ? "mora biti arm64 (Apple čip)" : "mora biti x86_64 (Intel)"}
cd ~/Downloads
${p.sha256 ? `echo "${p.sha256}  ${p.name}" | shasum -a 256 -c   # ${p.name}: OK\n` : ""}tar -xzf ${p.name}
sudo install -d -m 755 /usr/local/bin
sudo install -m 755 ${dirOf(p)}/shf /usr/local/bin/shf
${MAC_REST}`;
}

const PLATFORM: Record<string, string> = {
  "linux-amd64": "Linux x86-64",
  "darwin-arm64": "Mac sa Apple čipom",
  "darwin-amd64": "Mac sa Intel procesorom",
};

// DownloadLink is the package for one platform, or why there is none.
function DownloadLink({ d, os, arch }: { d: Downloads | null | undefined; os: string; arch: string }) {
  const p = pick(d, os, arch);
  if (d === undefined) return <p className="side-note">Tražim pakete na serveru…</p>;
  if (!p) {
    return (
      <div className="callout" style={{ marginBottom: 8 }}>
        <span>
          {d?.enabled
            ? `Na ovom serveru još nema paketa za: ${PLATFORM[`${os}-${arch}`] ?? `${os}-${arch}`}.`
            : "Ovaj server ne nudi pakete za preuzimanje."}{" "}
          Admin ih postavlja (<code>make release</code>, pa <code>dist/</code> u direktorijum{" "}
          <code>shfd serve --downloads</code>; <code>docs/install.md</code>), ili instaliraj iz izvornog koda.
        </span>
      </div>
    );
  }
  return (
    <div className="download" style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap", marginBottom: 8 }}>
      <a className="btn primary" href={p.url} download={p.name}>
        <Download size={15} /> Preuzmi {p.name}
      </a>
      <span className="side-note">
        {PLATFORM[`${os}-${arch}`] ?? `${os}-${arch}`} · verzija {p.version} · {formatSize(p.size)}
        {d?.sha256sums ? (
          <>
            {" "}
            · <a href={d.sha256sums}>SHA256SUMS</a>
          </>
        ) : null}
      </span>
    </div>
  );
}

const SRC: Record<Os, string> = {
  linux: `sudo apt install make git rsync openssh-client   # Fedora: dnf install golang make git rsync openssh-clients
# Go 1.27+ sa go.dev (apt-ov golang-go je obično stariji):
curl -fsSL https://go.dev/dl/go1.27.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin   # i u ~/.profile
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                       # bin/shf i bin/shfd
sudo install -m 755 bin/shf /usr/local/bin/shf`,
  macos: `xcode-select --install          # make, git
echo 'eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"' >> ~/.zprofile
source ~/.zprofile               # Homebrew ispred /usr/bin; radi i na Apple čipu i na Intel-u
brew install go rsync            # Go 1.27+; /usr/bin/rsync (openrsync) ne radi
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                       # bin/shf
sudo install -d -m 755 /usr/local/bin
sudo install -m 755 bin/shf /usr/local/bin/shf`,
  windows: `winget install GoLang.Go Git.Git   # Go 1.27+; make ne treba
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
go build -trimpath -o shf.exe ./cmd/shf
# rsync 3.2.4+ i ssh moraju biti u PATH-u (npr. MSYS2: pacman -S rsync openssh),
# ili ih zadaj apsolutnim putanjama u SHF_RSYNC i SHF_SSH`,
};

export function Install({ standalone }: { standalone?: boolean }) {
  const [tab, setTab] = useState<"client" | "server">("client");
  const [os, setOs] = useState<Os>(detectOs);
  const [how, setHow] = useState<How>("pkg");
  const [macArch, setMacArch] = useState<MacArch>("arm64");
  // undefined while loading, null when shfd does not answer (or predates /v1/downloads)
  const [downloads, setDownloads] = useState<Downloads | null | undefined>(undefined);
  useEffect(() => {
    let live = true;
    void getDownloads().then((d) => live && setDownloads(d));
    void detectMacArch().then((a) => live && setMacArch(a));
    return () => {
      live = false;
    };
  }, []);
  const pkgArch = os === "macos" ? macArch : "amd64";
  const pkg = os === "windows" ? undefined : pick(downloads, os === "macos" ? "darwin" : "linux", pkgArch);
  const showHow: How = os === "windows" ? "src" : how;
  const origin = location.origin;
  const app = useAppMaybe();
  const shareId = app?.me.share_id ?? "<share-id>";
  const folder = app ? `~/${app.me.share_name}` : "~/Tim";

  const body = (
    <div className="page install-wrap">
      <div className="page-head">
        <div>
          <h2>Instalacija</h2>
          <p>Kako se uređaj priključi folderu{standalone ? "" : ", i kako se podiže server"}.</p>
        </div>
        <div className="spacer" />
        {!standalone ? (
          <div className="tabs">
            <button className={tab === "client" ? "on" : ""} onClick={() => setTab("client")}>
              Klijent (shf)
            </button>
            <button className={tab === "server" ? "on" : ""} onClick={() => setTab("server")}>
              Server (shfd + shfd-gui)
            </button>
          </div>
        ) : null}
      </div>

      {tab === "client" ? (
        <div className="steps">
          <Step n={1} title="Instaliraj shf">
            <p>
              Gotov paket postoji za Linux x86-64, Mac sa Apple čipom i Mac sa Intel procesorom (
              <code>shared-folder-&lt;verzija&gt;-&lt;os&gt;-&lt;arch&gt;.tar.gz</code>,{" "}
              {downloads === null || downloads?.enabled === false ? "daje ga admin" : "preuzima se sa ovog servera"}; prave
              se sa <code>make release</code>). Na uređaju su potrebni još <code>ssh</code> i{" "}
              <b>rsync 3.2.4 ili noviji</b>.
            </p>
            <div className="tabs os-tabs" style={{ display: "inline-flex" }}>
              {(["linux", "macos", "windows"] as Os[]).map((o) => (
                <button key={o} className={os === o ? "on" : ""} onClick={() => setOs(o)}>
                  {o === "linux" ? "Linux" : o === "macos" ? "macOS" : "Windows"}
                </button>
              ))}
            </div>{" "}
            {os !== "windows" ? (
              <div className="tabs os-tabs" style={{ display: "inline-flex", marginLeft: 8 }}>
                <button className={how === "pkg" ? "on" : ""} onClick={() => setHow("pkg")}>
                  Gotov paket
                </button>
                <button className={how === "src" ? "on" : ""} onClick={() => setHow("src")}>
                  Iz izvornog koda
                </button>
              </div>
            ) : null}{" "}
            {os === "macos" && showHow === "pkg" ? (
              <div className="tabs os-tabs" style={{ display: "inline-flex", marginLeft: 8 }}>
                <button className={macArch === "arm64" ? "on" : ""} onClick={() => setMacArch("arm64")}>
                  Apple čip (M1, M2…)
                </button>
                <button className={macArch === "amd64" ? "on" : ""} onClick={() => setMacArch("amd64")}>
                  Intel
                </button>
              </div>
            ) : null}
            {showHow === "pkg" && os !== "windows" ? (
              <DownloadLink d={downloads} os={os === "macos" ? "darwin" : "linux"} arch={pkgArch} />
            ) : null}
            <Code>{showHow === "src" ? SRC[os] : os === "macos" ? pkgMac(macArch, pkg) : pkgLinux(pkg)}</Code>
            {showHow === "src" ? (
              <p className="side-note" style={{ marginTop: 6 }}>
                Za build trebaju <b>Go 1.27+</b>, <b>make</b> i <b>git</b> (na Windows-u make ne treba). Detalji po
                OS-u: <code>docs/install.md</code>.
              </p>
            ) : null}
            {os === "macos" && showHow === "pkg" ? (
              <div className="callout" style={{ marginTop: 8 }}>
                <span>
                  <b>Karantin.</b> Fajl preuzet browser-om macOS označava sa <code>com.apple.quarantine</code>, a paket nije
                  potpisan, pa Gatekeeper <code>shf</code> ne pušta (terminal kaže <code>killed</code>, launchd agent
                  tiho ne radi). Zato <code>xattr -d</code> iznad, posle svake instalacije ili nove verzije;{" "}
                  <code>xattr -l</code> ne sme da ispiše <code>com.apple.quarantine</code>.
                </span>
              </div>
            ) : null}
          </Step>
          <Step n={2} title="Pridruži se folderu">
            <p>
              Admin pravi člana za ovaj uređaj (strana <b>Članovi i tokeni</b> → <b>Nov član</b>) i daje ti token.
              Zatim:
            </p>
            <Code>{`shf join ${origin} ${shareId} <token> ${folder}`}</Code>
            <p className="side-note" style={{ marginTop: 6 }}>
              <code>join</code> u folder koji već ima fajlove samo spaja; ništa se ne briše.
            </p>
          </Step>
          <Step n={3} title="Pokreni pozadinski klijent">
            <Code>{`shf service install      # systemd user jedinica (Linux) ili launchd agent (macOS)
shf service status       # da li radi, poslednje linije loga`}</Code>
          </Step>
          <Step n={4} title="Proveri">
            <Code>{`shf status ${folder}`}</Code>
          </Step>
          <div className="callout">
            <span>
              <b>Jedan član = jedan uređaj.</b> Server čuva jedan SSH ključ po članu. Ako se isti token iskoristi na
              drugom uređaju, prvi uređaj dobija <code>state: key refused</code> u <code>shf status</code>. Rešenje: za
              svaki uređaj poseban član i token; na uređaju koji je izgubio ključ ponovo <code>shf join</code> sa
              sopstvenim tokenom.
            </span>
          </div>
        </div>
      ) : (
        <div className="steps">
          <Step n={1} title="shfd: paketi, korisnik, servis">
            <p>
              Ukratko iz <code>docs/server-ssh.md</code>: <code>openssh-server</code> i <code>rsync</code>, sistemski
              korisnik <code>shfd</code> sa pravom ljuskom, data-root i <code>authorized_keys</code> u njegovom
              vlasništvu.
            </p>
            <Code>{`sudo apt install openssh-server rsync
sudo useradd --system --create-home --home-dir /var/lib/shfd --shell /bin/sh shfd
sudo install -d -o shfd -g shfd -m 700 /var/lib/shfd/data /var/lib/shfd/.ssh
sudo install -o shfd -g shfd -m 600 /dev/null /var/lib/shfd/.ssh/authorized_keys
# /opt/shfd/versions/<sha>/shfd, symlink /opt/shfd/current, systemd shfd.service:
#   shfd serve --listen 127.0.0.1:8443 --ssh-endpoint shfd@<host>:22
# paketi za klijente (kartica Klijent): make release, pa dist/ u --downloads
sudo install -d -o shfd -g shfd -m 755 /var/lib/shfd/downloads
#   shfd serve … --downloads /var/lib/shfd/downloads   (ili SHFD_DOWNLOADS)`}</Code>
          </Step>
          <Step n={2} title="Prvi folder i admin">
            <Code>{`sudo -u shfd SHFD_DATA_ROOT=/var/lib/shfd/data shfd share create tim --admin ana
# ispisuje share-id i token prvog admina: tim tokenom se ulazi i u ovaj GUI`}</Code>
          </Step>
          <Step n={3} title="shfd-gui">
            <p>
              Poseban servis (Node 22) na <code>127.0.0.1:8444</code>; sve podatke dobija od shfd-a tokenom
              prijavljenog admina.
            </p>
            <Code>{`sudo apt install nodejs           # Node 22
cd shared-folder && make gui        # gui/dist: bin/shfd-gui, server.js, web/
sudo cp -r gui/dist /opt/shfd-gui/versions/<sha>
sudo ln -sfn versions/<sha> /opt/shfd-gui/current
sudo cp gui/deploy/shfd-gui.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now shfd-gui`}</Code>
          </Step>
          <Step n={4} title="nginx">
            <p>
              U vhost koji već prosleđuje na <code>127.0.0.1:8443</code> dodaj <code>gui/deploy/nginx-gui.conf</code>{" "}
              (<code>/gui/</code> i, bez baferovanja, <code>/gui/udc/changes</code>), pa{" "}
              <code>sudo nginx -t && sudo systemctl reload nginx</code>.
            </p>
          </Step>
          <Step n={5} title="Provera">
            <Code>{`curl -s ${origin}/healthz        # {"service":"shfd","version":…,"ok":true}
curl -s ${origin}/gui/healthz    # {"ok":true}`}</Code>
          </Step>
        </div>
      )}
    </div>
  );

  if (!standalone) return body;
  return (
    <div className="install-standalone">
      <header className="top" style={{ height: 56 }}>
        <div className="logo">
          <div className="logo-mark">
            <Folder size={18} />
          </div>
          <div>
            <b>{location.hostname.split(".")[0]}</b>
            <small>shfd GUI</small>
          </div>
        </div>
        <div className="spacer" />
        <a className="btn" href="#members">
          Prijava
        </a>
      </header>
      {body}
    </div>
  );
}
