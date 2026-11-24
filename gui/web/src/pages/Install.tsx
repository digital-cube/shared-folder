import { useEffect, useState, type ReactNode } from "react";
import { BASE } from "../api";
import { useAppMaybe } from "../App";
import { detectMacArch, dirOf, formatSize, getDownloads, pick, type Downloads, type MacArch, type Package } from "../downloads";
import { useTr } from "../i18n";
import { Download, Folder } from "../icons";
import { tr } from "../lang";
import { detectOs, type Os } from "../os";
import { Prefs } from "../Prefs";

function Code({ children }: { children: string }) {
  const [done, setDone] = useState(false);
  const tr = useTr();
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
        {done ? tr("Kopirano", "Copied") : tr("Kopiraj", "Copy")}
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
    return `cd ~/Downloads                   # shared-folder-<${tr("verzija", "version")}>-linux-amd64.tar.gz
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

const macRest = () => `sudo xattr -d com.apple.quarantine "$(command -v shf)"   # ${tr("skida karantin", "removes the quarantine")}
xattr -l "$(command -v shf)"     # ${tr("ne sme da ispiše com.apple.quarantine", "must not print com.apple.quarantine")}
echo 'eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"' >> ~/.zprofile
source ~/.zprofile               # ${tr("Homebrew ispred /usr/bin; radi i na Apple čipu i na Intel-u", "Homebrew before /usr/bin; works on Apple silicon and on Intel")}
brew install rsync               # ${tr("/usr/bin/rsync (openrsync) ne radi", "/usr/bin/rsync (openrsync) does not work")}
rsync --version | head -1        # rsync  version 3.x.y (${tr("ne openrsync", "not openrsync")})
shf version`;

function pkgMac(arch: MacArch, p?: Package): string {
  if (!p) {
    return `cd ~/Downloads                   # …-darwin-arm64 (M1, M2…) ${tr("ili", "or")} …-darwin-amd64 (Intel)
tar -xzf shared-folder-*-darwin-$(uname -m | sed s/x86_64/amd64/).tar.gz
sudo install -d -m 755 /usr/local/bin
sudo install -m 755 shared-folder-*-darwin-*/shf /usr/local/bin/shf
${macRest()}`;
  }
  return `uname -m                         # ${arch === "arm64" ? tr("mora biti arm64 (Apple čip)", "must be arm64 (Apple silicon)") : tr("mora biti x86_64 (Intel)", "must be x86_64 (Intel)")}
cd ~/Downloads
${p.sha256 ? `echo "${p.sha256}  ${p.name}" | shasum -a 256 -c   # ${p.name}: OK\n` : ""}tar -xzf ${p.name}
sudo install -d -m 755 /usr/local/bin
sudo install -m 755 ${dirOf(p)}/shf /usr/local/bin/shf
${macRest()}`;
}

function platform(os: string, arch: string): string {
  const k = `${os}-${arch}`;
  if (k === "linux-amd64") return "Linux x86-64";
  if (k === "darwin-arm64") return tr("Mac sa Apple čipom", "Mac with Apple silicon");
  if (k === "darwin-amd64") return tr("Mac sa Intel procesorom", "Mac with an Intel processor");
  return k;
}

// DownloadLink is the package for one platform, or why there is none.
function DownloadLink({ d, os, arch }: { d: Downloads | null | undefined; os: string; arch: string }) {
  const p = pick(d, os, arch);
  const tr = useTr();
  if (d === undefined) return <p className="side-note">{tr("Tražim pakete na serveru…", "Looking for packages on the server…")}</p>;
  if (!p) {
    return (
      <div className="callout" style={{ marginBottom: 8 }}>
        <span>
          {d?.enabled
            ? tr(`Na ovom serveru još nema paketa za: ${platform(os, arch)}.`, `This server has no package yet for: ${platform(os, arch)}.`)
            : tr("Ovaj server ne nudi pakete za preuzimanje.", "This server offers no packages for download.")}{" "}
          {tr("Admin ih postavlja (", "An admin puts them there (")}
          <code>make release</code>
          {tr(", pa ", ", then ")}
          <code>dist/</code>
          {tr(" u direktorijum ", " into the directory of ")}
          <code>shfd serve --downloads</code>; <code>docs/install.md</code>
          {tr("), ili instaliraj iz izvornog koda.", "), or install from source.")}
        </span>
      </div>
    );
  }
  return (
    <div className="download" style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap", marginBottom: 8 }}>
      <a className="btn primary" href={p.url} download={p.name}>
        <Download size={15} /> {tr("Preuzmi", "Download")} {p.name}
      </a>
      <span className="side-note">
        {platform(os, arch)} · {tr("verzija", "version")} {p.version} · {formatSize(p.size)}
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

const src = (os: Os): string => ({
  linux: `sudo apt install make git rsync openssh-client   # Fedora: dnf install golang make git rsync openssh-clients
# ${tr("Go 1.27+ sa go.dev (apt-ov golang-go je obično stariji):", "Go 1.27+ from go.dev (apt's golang-go is usually older):")}
curl -fsSL https://go.dev/dl/go1.27.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin   # ${tr("i u ~/.profile", "and in ~/.profile")}
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                       # bin/shf ${tr("i", "and")} bin/shfd
sudo install -m 755 bin/shf /usr/local/bin/shf`,
  macos: `xcode-select --install          # make, git
echo 'eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || /usr/local/bin/brew shellenv)"' >> ~/.zprofile
source ~/.zprofile               # ${tr("Homebrew ispred /usr/bin; radi i na Apple čipu i na Intel-u", "Homebrew before /usr/bin; works on Apple silicon and on Intel")}
brew install go rsync            # Go 1.27+; ${tr("/usr/bin/rsync (openrsync) ne radi", "/usr/bin/rsync (openrsync) does not work")}
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                       # bin/shf
sudo install -d -m 755 /usr/local/bin
sudo install -m 755 bin/shf /usr/local/bin/shf`,
  windows: `winget install GoLang.Go Git.Git   # Go 1.27+; ${tr("make ne treba", "make is not needed")}
git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
go build -trimpath -o shf.exe ./cmd/shf
# ${tr("rsync 3.2.4+ i ssh moraju biti u PATH-u (npr. MSYS2: pacman -S rsync openssh),", "rsync 3.2.4+ and ssh must be on the PATH (e.g. MSYS2: pacman -S rsync openssh),")}
# ${tr("ili ih zadaj apsolutnim putanjama u SHF_RSYNC i SHF_SSH", "or give their absolute paths in SHF_RSYNC and SHF_SSH")}`,
})[os];

export function Install({ standalone }: { standalone?: boolean }) {
  const tr = useTr();
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
          <h2>{tr("Instalacija", "Install")}</h2>
          <p>
            {standalone
              ? tr("Kako se uređaj priključi folderu.", "How a device joins the folder.")
              : tr("Kako se uređaj priključi folderu, i kako se podiže server.", "How a device joins the folder, and how to set up the server.")}
          </p>
        </div>
        <div className="spacer" />
        {!standalone ? (
          <div className="tabs">
            <button className={tab === "client" ? "on" : ""} onClick={() => setTab("client")}>
              {tr("Klijent (shf)", "Client (shf)")}
            </button>
            <button className={tab === "server" ? "on" : ""} onClick={() => setTab("server")}>
              Server (shfd + shfd-gui)
            </button>
          </div>
        ) : null}
      </div>

      {tab === "client" ? (
        <div className="steps">
          <Step n={1} title={tr("Instaliraj shf", "Install shf")}>
            <p>
              {tr(
                "Gotov paket postoji za Linux x86-64, Mac sa Apple čipom i Mac sa Intel procesorom (",
                "Ready-made packages exist for Linux x86-64, Mac with Apple silicon and Mac with an Intel processor (",
              )}
              <code>shared-folder-&lt;{tr("verzija", "version")}&gt;-&lt;os&gt;-&lt;arch&gt;.tar.gz</code>,{" "}
              {downloads === null || downloads?.enabled === false
                ? tr("daje ga admin", "an admin hands it out")
                : tr("preuzima se sa ovog servera", "downloaded from this server")}
              {tr("; prave se sa ", "; built with ")}
              <code>make release</code>
              {tr("). Na uređaju su potrebni još ", "). The device also needs ")}
              <code>ssh</code> {tr("i", "and")} <b>{tr("rsync 3.2.4 ili noviji", "rsync 3.2.4 or newer")}</b>.
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
                  {tr("Gotov paket", "Package")}
                </button>
                <button className={how === "src" ? "on" : ""} onClick={() => setHow("src")}>
                  {tr("Iz izvornog koda", "From source")}
                </button>
              </div>
            ) : null}{" "}
            {os === "macos" && showHow === "pkg" ? (
              <div className="tabs os-tabs" style={{ display: "inline-flex", marginLeft: 8 }}>
                <button className={macArch === "arm64" ? "on" : ""} onClick={() => setMacArch("arm64")}>
                  {tr("Apple čip (M1, M2…)", "Apple silicon (M1, M2…)")}
                </button>
                <button className={macArch === "amd64" ? "on" : ""} onClick={() => setMacArch("amd64")}>
                  Intel
                </button>
              </div>
            ) : null}
            {showHow === "pkg" && os !== "windows" ? (
              <DownloadLink d={downloads} os={os === "macos" ? "darwin" : "linux"} arch={pkgArch} />
            ) : null}
            <Code>{showHow === "src" ? src(os) : os === "macos" ? pkgMac(macArch, pkg) : pkgLinux(pkg)}</Code>
            {showHow === "src" ? (
              <p className="side-note" style={{ marginTop: 6 }}>
                {tr("Za build trebaju ", "The build needs ")}
                <b>Go 1.27+</b>, <b>make</b> {tr("i", "and")} <b>git</b>{" "}
                {tr("(na Windows-u make ne treba). Detalji po OS-u: ", "(make is not needed on Windows). Details per OS: ")}
                <code>docs/install.md</code>.
              </p>
            ) : null}
            {os === "macos" && showHow === "pkg" ? (
              <div className="callout" style={{ marginTop: 8 }}>
                <span>
                  <b>{tr("Karantin.", "Quarantine.")}</b>{" "}
                  {tr("Fajl preuzet browser-om macOS označava sa ", "macOS marks a file downloaded by a browser with ")}
                  <code>com.apple.quarantine</code>
                  {tr(", a paket nije potpisan, pa Gatekeeper ", ", and the package is not signed, so Gatekeeper does not let ")}
                  <code>shf</code>
                  {tr(" ne pušta (terminal kaže ", " run (the terminal says ")}
                  <code>killed</code>
                  {tr(", launchd agent tiho ne radi). Zato ", ", the launchd agent silently does not work). Hence ")}
                  <code>xattr -d</code>
                  {tr(" iznad, posle svake instalacije ili nove verzije; ", " above, after every install or new version; ")}
                  <code>xattr -l</code> {tr("ne sme da ispiše", "must not print")} <code>com.apple.quarantine</code>.
                </span>
              </div>
            ) : null}
          </Step>
          <Step n={2} title={tr("Pridruži se folderu", "Join the folder")}>
            <p>
              {tr("Admin pravi člana za ovaj uređaj (strana ", "An admin creates a member for this device (page ")}
              <b>{tr("Članovi i tokeni", "Members and tokens")}</b> → <b>{tr("Nov član", "New member")}</b>
              {tr(") i daje ti token. Zatim:", ") and gives you the token. Then:")}
            </p>
            <Code>{`shf join ${origin} ${shareId} <token> ${folder}`}</Code>
            <p className="side-note" style={{ marginTop: 6 }}>
              <code>join</code>{" "}
              {tr("u folder koji već ima fajlove samo spaja; ništa se ne briše.", "into a folder that already has files only merges; nothing is deleted.")}
            </p>
          </Step>
          <Step n={3} title={tr("Pokreni pozadinski klijent", "Start the background client")}>
            <Code>{`shf service install      # ${tr("systemd user jedinica (Linux) ili launchd agent (macOS)", "systemd user unit (Linux) or launchd agent (macOS)")}
shf service status       # ${tr("da li radi, poslednje linije loga", "whether it runs, the last lines of the log")}`}</Code>
          </Step>
          <Step n={4} title={tr("Proveri", "Check")}>
            <Code>{`shf status ${folder}`}</Code>
          </Step>
          <div className="callout">
            <span>
              <b>{tr("Jedan član = jedan uređaj.", "One member = one device.")}</b>{" "}
              {tr(
                "Server čuva jedan SSH ključ po članu. Ako se isti token iskoristi na drugom uređaju, prvi uređaj dobija ",
                "The server keeps one SSH key per member. If the same token is used on a second device, the first device gets ",
              )}
              <code>state: key refused</code> {tr("u", "in")} <code>shf status</code>
              {tr(
                ". Rešenje: za svaki uređaj poseban član i token; na uređaju koji je izgubio ključ ponovo ",
                ". The fix: a separate member and token for every device; on the device that lost its key, run ",
              )}
              <code>shf join</code> {tr("sa sopstvenim tokenom.", "again with its own token.")}
            </span>
          </div>
        </div>
      ) : (
        <div className="steps">
          <Step n={1} title={tr("shfd: paketi, korisnik, servis", "shfd: packages, user, service")}>
            <p>
              {tr("Ukratko iz ", "In short, from ")}
              <code>docs/server-ssh.md</code>: <code>openssh-server</code> {tr("i", "and")} <code>rsync</code>
              {tr(", sistemski korisnik ", ", a system user ")}
              <code>shfd</code>
              {tr(" sa pravom ljuskom, data-root i ", " with a real shell, the data-root and ")}
              <code>authorized_keys</code> {tr("u njegovom vlasništvu.", "owned by it.")}
            </p>
            <Code>{`sudo apt install openssh-server rsync
sudo useradd --system --create-home --home-dir /var/lib/shfd --shell /bin/sh shfd
sudo install -d -o shfd -g shfd -m 700 /var/lib/shfd/data /var/lib/shfd/.ssh
sudo install -o shfd -g shfd -m 600 /dev/null /var/lib/shfd/.ssh/authorized_keys
# /opt/shfd/versions/<sha>/shfd, symlink /opt/shfd/current, systemd shfd.service:
#   shfd serve --listen 127.0.0.1:8443 --ssh-endpoint shfd@<host>:22
# ${tr("paketi za klijente (kartica Klijent): make release, pa dist/ u --downloads", "packages for the clients (Client tab): make release, then dist/ into --downloads")}
sudo install -d -o shfd -g shfd -m 755 /var/lib/shfd/downloads
#   shfd serve … --downloads /var/lib/shfd/downloads   (${tr("ili", "or")} SHFD_DOWNLOADS)`}</Code>
          </Step>
          <Step n={2} title={tr("Prvi folder i admin", "The first folder and admin")}>
            <Code>{`sudo -u shfd SHFD_DATA_ROOT=/var/lib/shfd/data shfd share create tim --admin ana
# ${tr("ispisuje share-id i token prvog admina: tim tokenom se ulazi i u ovaj GUI", "prints the share-id and the first admin's token: that token also signs in to this GUI")}`}</Code>
          </Step>
          <Step n={3} title="shfd-gui">
            <p>
              {tr("Poseban servis (Node 22) na ", "A separate service (Node 22) on ")}
              <code>127.0.0.1:8444</code>
              {tr("; sve podatke dobija od shfd-a tokenom prijavljenog admina.", "; it gets all its data from shfd with the token of the signed-in admin.")}
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
              {tr("GUI na korenu hosta (", "The GUI at the root of the host (")}
              <code>SHFD_GUI_BASE=</code>
              {tr("): vhost po ", "): a vhost after ")}
              <code>gui/deploy/nginx-gui-root.conf</code> — <code>/v1/</code>, <code>/healthz</code> {tr("i", "and")}{" "}
              <code>/downloads/</code>
              {tr(" idu shfd-u, sve ostalo GUI-ju. GUI pod ", " go to shfd, everything else to the GUI. The GUI under ")}
              <code>/gui</code>
              {tr(": u vhost koji već prosleđuje na ", ": to the vhost that already proxies to ")}
              <code>127.0.0.1:8443</code> {tr("dodaj", "add")} <code>gui/deploy/nginx-gui.conf</code>
              {tr(". U oba slučaja ", ". Either way ")}
              <code>&lt;base&gt;/udc/changes</code>
              {tr(" ide bez baferovanja, pa ", " goes unbuffered, then ")}
              <code>sudo nginx -t && sudo systemctl reload nginx</code>.
            </p>
          </Step>
          <Step n={5} title={tr("Provera", "Check")}>
            <Code>{`curl -s ${origin}/healthz        # {"service":"shfd","version":…,"ok":true}
curl -s ${origin}${BASE}/api/healthz    # {"service":"shfd-gui","ok":true}`}</Code>
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
        <Prefs />
        <a className="btn" href="#members">
          {tr("Prijava", "Sign in")}
        </a>
      </header>
      {body}
    </div>
  );
}
