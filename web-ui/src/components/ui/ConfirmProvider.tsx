import { useCallback, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { ConfirmContext, type ConfirmOptions } from "../../lib/confirm";
import { Button } from "./Button";
import { Modal } from "./Modal";

// ConfirmProvider — единый модальный диалог подтверждения вместо нативного
// window.confirm (QA-2026-02 / П16): нативный confirm показывал «Подтвердите
// действие на сайте <host>» и плохо читался. Монтируется один раз в App;
// дочерние компоненты вызывают useConfirm().
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [opts, setOpts] = useState<ConfirmOptions | null>(null);
  const resolverRef = useRef<((ok: boolean) => void) | null>(null);

  const confirm = useCallback((o: ConfirmOptions) => {
    setOpts(o);
    return new Promise<boolean>((resolve) => {
      resolverRef.current = resolve;
    });
  }, []);

  const settle = useCallback((ok: boolean) => {
    resolverRef.current?.(ok);
    resolverRef.current = null;
    setOpts(null);
  }, []);

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {opts && (
        <Modal
          title={opts.title}
          onClose={() => settle(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => settle(false)}>
                {opts.cancelLabel ?? t("common.cancel")}
              </Button>
              <Button variant={opts.danger ? "danger" : "primary"} onClick={() => settle(true)}>
                {opts.confirmLabel ?? t("common.confirm")}
              </Button>
            </>
          }
        >
          {opts.message && <p className="text-sm text-fg-muted">{opts.message}</p>}
        </Modal>
      )}
    </ConfirmContext.Provider>
  );
}
