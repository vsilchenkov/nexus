// UI-kit панели (§21, эталон specs/nexus_ui.html). Единый источник атомов.
export { Button } from "./Button";
export { CopyButton } from "./CopyButton";
export { Input, Select, Textarea, Field } from "./form";
export { SecretInput } from "./SecretInput";
export { SearchInput } from "./SearchInput";
export { Card, SectionHead } from "./surfaces";
export { Modal } from "./Modal";
export { ConfirmProvider } from "./ConfirmProvider";
export { Chip, Pill, Kpi, KpiRow, Seg, Hint } from "./data";
export type { SegOption } from "./data";
export { ErrorAlert } from "./ErrorAlert";
export { LabelHint } from "./LabelHint";
export { PeriodPicker } from "./PeriodPicker";
export { DefaultPeriodButton } from "./DefaultPeriodButton";
export { DateTimeField } from "./DateTimeField";
export { LatencyChart, type LatencyPoint } from "./LatencyChart";
export { periodParams, periodKey, periodLabel, periodWindow, defaultPeriod, PRESET_RANGES } from "../../lib/period";
export type { Period, PresetRange } from "../../lib/period";
export { PickGroup, Toggle, Toggle3 } from "./pickers";
export type { PickOption } from "./pickers";
export { TrafficChart } from "./TrafficChart";
export type { SeriesPoint, LogsRange } from "./TrafficChart";
export { ChartTooltip } from "./ChartTooltip";
export type { ChartTooltipProps, TooltipSeries, TooltipMarker } from "./ChartTooltip";
// Radix/cmdk обёртки (§23–§25): popover, tooltip, combobox.
export { Popover, PopoverTrigger, PopoverAnchor, PopoverContent } from "./Popover";
export { Tooltip, TooltipProvider } from "./Tooltip";
export {
  Command,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
} from "./Command";
