import { UdcClient } from "@jsudc/core/client";
import { createRendererRegistry, createUdcQueryClient, UdcI18nProvider, UdcProvider } from "@jsudc/react";
import { tableRenderer } from "@jsudc/react/table";
import { Toaster, TooltipProvider, UDC_SCOPE_CLASS } from "@jsudc/react/ui";
import { QueryClientProvider } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { BASE, getSession, logout, type Me } from "./api";
import { cells } from "./cells";
import { messages } from "./messages";
import { Login } from "./pages/Login";
import { Install } from "./pages/Install";
import { Shell } from "./Shell";

export type Page = "members" | "logs" | "files" | "install";
const PAGES: Page[] = ["members", "logs", "files", "install"];

function pageFromHash(): Page {
  const h = location.hash.replace(/^#\/?/, "").split(/[/?]/)[0] as Page;
  return PAGES.includes(h) ? h : "members";
}

export function usePage(): [Page, (p: Page) => void] {
  const [page, setPage] = useState<Page>(pageFromHash);
  useEffect(() => {
    const on = () => setPage(pageFromHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return [page, (p) => (location.hash = `#${p}`)];
}

interface Live {
  paused: boolean;
  setPaused: (p: boolean) => void;
  me: Me;
  signOut: () => void;
}
const LiveContext = createContext<Live | null>(null);
export const useApp = () => useContext(LiveContext)!;
/** The signed-in app, or null on a page shown without a sign-in. */
export const useAppMaybe = () => useContext(LiveContext);

const client = new UdcClient({ httpUrl: BASE });
const renderers = createRendererRegistry(tableRenderer);

export function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const [page] = usePage();
  const [expired, setExpired] = useState(false);

  useEffect(() => {
    getSession().then(setMe, () => setMe(null));
  }, []);

  const qc = useMemo(
    () =>
      createUdcQueryClient({
        onUnauthenticated: () => {
          setExpired(true);
          setMe(null);
        },
      }),
    [],
  );

  const signOut = useCallback(() => {
    void logout().finally(() => {
      qc.clear();
      setMe(null);
    });
  }, [qc]);

  if (me === undefined) return null;
  if (me === null) {
    if (page === "install") return <Install standalone />;
    return (
      <Login
        expired={expired}
        onIn={(m) => {
          setExpired(false);
          qc.clear();
          setMe(m);
        }}
      />
    );
  }
  return (
    <QueryClientProvider client={qc}>
      <Signed me={me} signOut={signOut} />
    </QueryClientProvider>
  );
}

function Signed({ me, signOut }: { me: Me; signOut: () => void }) {
  const [paused, setPaused] = useState(false);
  return (
    <UdcProvider client={client} renderers={renderers} cells={cells} changes={!paused}>
      <UdcI18nProvider locale="sr" messages={messages}>
        <TooltipProvider>
          <Toaster>
            <LiveContext.Provider value={{ paused, setPaused, me, signOut }}>
              <div className={UDC_SCOPE_CLASS} style={{ height: "100%" }}>
                <Shell />
              </div>
            </LiveContext.Provider>
          </Toaster>
        </TooltipProvider>
      </UdcI18nProvider>
    </UdcProvider>
  );
}
