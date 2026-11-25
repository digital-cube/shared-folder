import { useEffect, useState } from "react";
import type { FieldProblem } from "./forms";

/**
 * The message of a form's check under the wrong field (docs/gui.md, "Pravila
 * za klik i forme"): `check` on submit shows it and moves the focus to that
 * field, typing in the field takes it away, `aria` and `hint` go on the
 * field. `ids` are the ids of the fields' inputs.
 */
export function useFieldProblem<F extends string>(ids: Record<F, string>) {
  const [problem, setProblem] = useState<FieldProblem<F>>(null);

  // A new object on every failed submit, so the focus moves again on a second click.
  useEffect(() => {
    if (problem) document.getElementById(ids[problem.field])?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [problem]);

  return {
    problem,
    /** Shows the problem of a submit; true when there is one and the submit stops. */
    check(p: FieldProblem<F>): boolean {
      setProblem(p);
      return p !== null;
    },
    clear() {
      setProblem(null);
    },
    /** Typing in a field takes its message away. */
    typed(field: F) {
      setProblem((p) => (p?.field === field ? null : p));
    },
    /** aria-invalid and aria-describedby of a field's input. */
    aria(field: F) {
      const on = problem?.field === field;
      return { "aria-invalid": on ? true : undefined, "aria-describedby": on ? `${ids[field]}-err` : undefined };
    },
    /** The message under a field, when it is that field's. */
    hint(field: F) {
      return problem?.field === field ? (
        <span id={`${ids[field]}-err`} className="hint err-hint" role="alert">
          {problem.text}
        </span>
      ) : null;
    },
  };
}
