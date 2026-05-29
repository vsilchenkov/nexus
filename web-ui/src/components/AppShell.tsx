import { Outlet } from "react-router-dom";

import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";
import { TooltipProvider } from "./ui";

// AppShell — каркас панели (§21): постоянный левый сайдбар + main (топбар +
// прокручиваемый контент). Оборачивает все защищённые маршруты через <Outlet/>.
// TooltipProvider монтируется один раз здесь (§25) — для tooltip'ов топбара.
export function AppShell() {
  return (
    <TooltipProvider delayDuration={400}>
      <div className="flex h-screen bg-app text-fg">
        <Sidebar />
        <div className="flex min-w-0 flex-1 flex-col">
          <Topbar />
          <main className="flex-1 overflow-auto p-6">
            <Outlet />
          </main>
        </div>
      </div>
    </TooltipProvider>
  );
}
