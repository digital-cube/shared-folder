// Requests of a page of the GUI: inject with the Origin a browser sends on
// a POST (csrf.ts refuses one without it). A test that sets origin or
// referer itself keeps its own.

import type { FastifyInstance } from "fastify";

export const PAGE_ORIGIN = "http://localhost";

export function fromPage<T extends FastifyInstance>(app: T): T {
  const inject = app.inject.bind(app) as (o: unknown) => unknown;
  (app as unknown as { bareInject: unknown }).bareInject = inject;
  (app as unknown as { inject: (o: unknown) => unknown }).inject = (o: unknown) => {
    if (o && typeof o === "object") {
      const opts = o as { headers?: Record<string, unknown> };
      const h = opts.headers ?? {};
      if (!("origin" in h) && !("referer" in h)) opts.headers = { ...h, origin: PAGE_ORIGIN };
    }
    return inject(o);
  };
  return app;
}

/** inject as it was: no Origin added. */
export function bare(app: FastifyInstance): FastifyInstance["inject"] {
  return (app as unknown as { bareInject: FastifyInstance["inject"] }).bareInject;
}
