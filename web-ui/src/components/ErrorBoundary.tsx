import { Component, type ErrorInfo, type ReactNode } from "react";

import i18n from "../i18n";
import { Button, Card } from "./ui";

// ErrorBoundary — перехватывает ошибки рендера в поддереве, чтобы один упавший
// компонент не гасил весь UI чёрным экраном. React error boundaries обязаны
// быть class-компонентами (нет hook-эквивалента для componentDidCatch), поэтому
// i18n берём через инстанс i18n.t() напрямую, а не через useTranslation-hook.
type Props = { children: ReactNode };
type State = { error: Error | null };

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // В консоль — для диагностики (источник/стек компонентов).
    console.error("ErrorBoundary caught:", error, info.componentStack);
  }

  render(): ReactNode {
    const { error } = this.state;
    if (!error) {
      return this.props.children;
    }
    // location.reload / href="/" — заодно сбрасывают упавшее состояние полным
    // перезапуском SPA.
    return (
      <div className="grid min-h-screen place-items-center bg-app p-6 text-fg">
        <Card className="max-w-lg space-y-4">
          <div className="space-y-1">
            <h1 className="text-lg font-semibold">{i18n.t("error_boundary.title")}</h1>
            <p className="text-sm text-fg-muted">{i18n.t("error_boundary.hint")}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" onClick={() => location.reload()}>
              {i18n.t("error_boundary.reload")}
            </Button>
            <Button onClick={() => (location.href = "/")}>{i18n.t("error_boundary.home")}</Button>
          </div>
          {error.message && (
            <details className="text-xs">
              <summary className="cursor-pointer text-fg-muted">
                {i18n.t("error_boundary.details")}
              </summary>
              <pre className="mt-2 overflow-auto rounded bg-bg-muted/40 p-2 text-[10px] text-err">
                {error.message}
              </pre>
            </details>
          )}
        </Card>
      </div>
    );
  }
}
