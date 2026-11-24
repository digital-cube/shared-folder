import { UdcError, unauthenticatedError } from "@jsudc/core";
import { tr, type Lang } from "./lang.js";
import { errorMessage, ShfdError } from "./shfd.js";

/**
 * A refusal of shfd as a UdcError whose message is a sentence for the UI.
 * 401 (token revoked or reset) signs the browser out.
 */
export function toUdcError(err: unknown, lang: Lang = "sr"): UdcError {
  if (err instanceof UdcError) return err;
  if (err instanceof ShfdError) {
    if (err.status === 401) return unauthenticatedError();
    const msg = errorMessage(err.code, err.message, lang);
    if (err.status === 403) return new UdcError("forbidden", msg, { code: err.code });
    if (err.status === 404) return new UdcError("not_found", msg, { code: err.code });
    if (err.status === 409) return new UdcError("version_conflict", msg, { code: err.code });
    if (err.status >= 500) return new UdcError("busy", msg, { code: err.code });
    return new UdcError("validation_error", msg, { code: err.code });
  }
  return new UdcError("internal", tr(lang, "Nešto nije uspelo.", "Something went wrong."));
}
