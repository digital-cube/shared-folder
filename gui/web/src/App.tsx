import { UdcClient } from "@jsudc/core/client";
import { createRendererRegistry, createUdcQueryClient, ThemeProvider, UdcI18nProvider, UdcProvider } from "@jsudc/react";
import { tableRenderer } from "@jsudc/react/table";
import { Toaster, TooltipProvider, UDC_SCOPE_CLASS } from "@jsudc/react/ui";
import { QueryClientProvider, useQuery, type QueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { autoFolder, defaultPage, pageFor, PAGES, rememberedFolder, rememberFolder, type Page } from "./access";
import { BASE, chooseFolder, getAccount, getSession, logout, type Me } from "./api";
import { cells } from "./cells";
import { LANGS, LangProvider, useLang } from "./i18n";
import { getLang, LANG_HEADER } from "./lang";
import { messages } from "./messages";
import { ChangePassword } from "./pages/Account";
import { Install } from "./pages/Install";
import { Login } from "./pages/Login";
import { AccountShell, Shell } from "./Shell";

export type { Page } from "./access";

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

/** The signed-in session, for every page: shown with or without a folder. */
interface SessionApi {
  me: Me;
  /** The session changed (a folder chosen, the password changed). */
  setMe: (m: Me) => void;
  /** Opens a folder of the account and remembers the choice in this browser. */
  openFolder: (shareId: string) => Promise<string | null>;
  signOut: () => void;
  /** The server already ended the session (this browser's device was revoked). */
  signedOut: () => void;
}
const SessionContext = createContext<SessionApi | null>(null);
export const useSession = () => useContext(SessionContext)!;

/** The account (GET /api/me): folders, devices, password rules. */
export function useAccount(enabled = true) {
  return useQuery({
    queryKey: ["account"],
    queryFn: async () => {
      const r = await getAccount();
      if (!r.ok) throw new Error(r.error);
      return r.data;
    },
    enabled,
    staleTime: 10_000,
  });
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

// Every call says the language of the UI, so shfd's refusals come back in it.
const client = new UdcClient({ httpUrl: BASE, headers: () => ({ [LANG_HEADER]: getLang() }) });
const renderers = createRendererRegistry(tableRenderer);

export function App() {
  return (
    <ThemeProvider>
      <LangProvider>
        <Root />
      </LangProvider>
    </ThemeProvider>
  );
}

function Root() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const [page] = usePage();
  const [expired, setExpired] = useState(false);
  // A fresh sign-in opens the only (or the remembered) folder by itself.
  const fresh = useRef(false);

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
      setExpired(false);
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
          fresh.current = true;
          setMe(m);
        }}
      />
    );
  }
  return (
    <QueryClientProvider client={qc}>
      <SignedIn me={me} setMe={setMe} qc={qc} signOut={signOut} fresh={fresh} />
    </QueryClientProvider>
  );
}

function SignedIn({
  me,
  setMe,
  qc,
  signOut,
  fresh,
}: {
  me: Me;
  setMe: (m: Me | null) => void;
  qc: QueryClient;
  signOut: () => void;
  fresh: React.MutableRefObject<boolean>;
}) {
  const api: SessionApi = useMemo(
    () => ({
      me,
      setMe: (m) => setMe(m),
      openFolder: async (shareId) => {
        const r = await chooseFolder(shareId);
        if (!r.ok) return r.error;
        rememberFolder(me.username, shareId);
        // Another folder: nothing of the last one may stay on screen.
        qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "account" });
        location.hash = `#${defaultPage(r.data)}`;
        setMe(r.data);
        return null;
      },
      signOut,
      signedOut: () => {
        qc.clear();
        setMe(null);
      },
    }),
    [me, setMe, qc, signOut],
  );

  let body: React.ReactNode;
  if (me.must_change) body = <ChangePassword forced />;
  else if (!me.share_id && me.kind === "account") body = <Chooser fresh={fresh} />;
  else body = <Signed key={me.share_id} me={me} signOut={signOut} />;
  return <SessionContext.Provider value={api}>{body}</SessionContext.Provider>;
}

/**
 * An account without a chosen folder: right after a sign-in the only
 * folder, or the one chosen last time in this browser, opens by itself;
 * otherwise "Moji folderi" with Moj nalog beside it.
 */
function Chooser({ fresh }: { fresh: React.MutableRefObject<boolean> }) {
  const { me, openFolder } = useSession();
  const account = useAccount();
  const [trying, setTrying] = useState(fresh.current);
  useEffect(() => {
    if (!fresh.current || !account.data) return;
    fresh.current = false;
    const id = autoFolder(account.data.folders, rememberedFolder(me.username));
    if (id) void openFolder(id).then(() => setTrying(false));
    else setTrying(false);
  }, [account.data, me.username, openFolder, fresh]);
  if (trying && !account.isError) return null;
  return <AccountShell />;
}

function Signed({ me, signOut }: { me: Me; signOut: () => void }) {
  const [paused, setPaused] = useState(false);
  const { lang, setLang } = useLang();
  const [page, go] = usePage();
  // A page the session may not see (a member on #members) opens its default.
  const shown = pageFor(me, page);
  useEffect(() => {
    if (shown !== page) go(shown);
  }, [shown, page, go]);
  return (
    <UdcProvider client={client} renderers={renderers} cells={cells} changes={!paused}>
      <UdcI18nProvider locale={lang} onLocaleChange={(l) => setLang(l === "en" ? "en" : "sr")} locales={LANGS} messages={messages}>
        <TooltipProvider>
          <Toaster>
            <LiveContext.Provider value={{ paused, setPaused, me, signOut }}>
              <div className={UDC_SCOPE_CLASS} style={{ height: "100%" }}>
                <Shell page={shown} />
              </div>
            </LiveContext.Provider>
          </Toaster>
        </TooltipProvider>
      </UdcI18nProvider>
    </UdcProvider>
  );
}
