import "@jsudc/react/styles.scoped.css";
import "./proto.css";
import "./app.css";
import { LUCIDE_ICONS } from "@jsudc/react";
import { Download, Undo2 } from "lucide-react";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

// The icons of our bulk actions (collections.ts) that jsudc's bar does not
// carry itself: it looks a name up here, and draws no icon for one it lacks.
Object.assign(LUCIDE_ICONS, { download: Download, "undo-2": Undo2 });

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
