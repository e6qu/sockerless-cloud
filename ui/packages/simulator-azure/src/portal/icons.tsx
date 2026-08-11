import type { FluentIcon } from "@fluentui/react-icons";
import {
  NavigationRegular,
  SearchRegular,
  ArrowClockwiseRegular,
  DeleteRegular,
  TagRegular,
  ArrowDownloadRegular,
  ChatBubblesQuestionRegular,
  AddRegular,
  CopyRegular,
  ChevronDownRegular,
  ChevronRightRegular,
  WeatherSunnyRegular,
  WeatherMoonRegular,
  CheckmarkCircleFilled,
  DismissCircleFilled,
  WarningFilled,
  CircleFilled,
  ProhibitedRegular,
  PlayRegular,
  StopRegular,
  AlertRegular,
  QuestionCircleRegular,
  CloudSyncRegular,
  EditRegular,
  SettingsRegular,
} from "@fluentui/react-icons";

// The real Fluent System Icons set (@fluentui/react-icons, Microsoft,
// MIT-licensed) — the same icon library Fluent UI React and the real Azure
// portal draw from — replacing the hand-drawn SVG paths this module used to
// carry. Every name below is a *resizable* variant (no numeric suffix, e.g.
// `AddRegular` rather than `Add20Regular`): only resizable variants honour a
// `fontSize` override at render time, which is what lets one AzureIconName
// serve every size this portal asks for (12px badges through 18px header
// buttons) — a *sized* variant like `Add20Regular` ignores that prop and
// always renders at its fixed design size.
const ICONS = {
  menu: NavigationRegular,
  search: SearchRegular,
  refresh: ArrowClockwiseRegular,
  delete: DeleteRegular,
  tag: TagRegular,
  download: ArrowDownloadRegular,
  feedback: ChatBubblesQuestionRegular,
  add: AddRegular,
  copy: CopyRegular,
  chevron_down: ChevronDownRegular,
  chevron_right: ChevronRightRegular,
  sun: WeatherSunnyRegular,
  moon: WeatherMoonRegular,
  status_success: CheckmarkCircleFilled,
  status_error: DismissCircleFilled,
  status_warning: WarningFilled,
  status_inactive: CircleFilled,
  not_supported: ProhibitedRegular,
  play: PlayRegular,
  stop: StopRegular,
  notifications: AlertRegular,
  help: QuestionCircleRegular,
  cloud_shell: CloudSyncRegular,
  edit: EditRegular,
  settings: SettingsRegular,
} satisfies Record<string, FluentIcon>;

export type AzureIconName = keyof typeof ICONS;

export function AzureIcon({ name, size }: { name: AzureIconName; size?: number | string }) {
  const Icon = ICONS[name];
  return <Icon aria-hidden focusable={false} fontSize={size ?? "1em"} />;
}
