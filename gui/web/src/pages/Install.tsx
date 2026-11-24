import { useState, type ReactNode } from "react";
import { useAppMaybe } from "../App";
import { Folder } from "../icons";

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

type Os = "linux" | "macos" | "windows";
const BUILD: Record<Os, string> = {
  linux: `git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                       # bin/shf i bin/shfd (Go 1.27+)
sudo install -m 755 bin/shf /usr/local/bin/shf
sudo apt install rsync openssh-client   # rsync 3.2.4+`,
  macos: `git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
make build                       # bin/shf (Go 1.27+)
sudo install -m 755 bin/shf /usr/local/bin/shf
brew install rsync               # /usr/bin/rsync (openrsync) ne radi
eval "$(/opt/homebrew/bin/brew shellenv)"   # i u ~/.zprofile`,
  windows: `git clone https://github.com/digital-cube/shared-folder.git
cd shared-folder
go build -o shf.exe ./cmd/shf    # Go 1.27+
# rsync 3.2.4+ i ssh moraju biti u PATH-u (npr. MSYS2: pacman -S rsync openssh),
# ili ih zadaj apsolutnim putanjama u SHF_RSYNC i SHF_SSH`,
};

export function Install({ standalone }: { standalone?: boolean }) {
  const [tab, setTab] = useState<"client" | "server">("client");
  const [os, setOs] = useState<Os>("linux");
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
          <Step n={1} title="Preuzmi shf">
            <p>
              Za sada se <code>shf</code> pravi iz izvornog koda (gotovih binarnih fajlova i <code>install.sh</code> još
              nema). Potrebni su još <code>ssh</code> i <b>rsync 3.2.4 ili noviji</b>.
            </p>
            <div className="tabs os-tabs" style={{ display: "inline-flex" }}>
              {(["linux", "macos", "windows"] as Os[]).map((o) => (
                <button key={o} className={os === o ? "on" : ""} onClick={() => setOs(o)}>
                  {o === "linux" ? "Linux" : o === "macos" ? "macOS" : "Windows"}
                </button>
              ))}
            </div>
            <Code>{BUILD[os]}</Code>
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
#   shfd serve --listen 127.0.0.1:8443 --ssh-endpoint shfd@<host>:22`}</Code>
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
