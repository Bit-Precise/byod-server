import {
  StrictMode,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { createRoot } from "react-dom/client";
import type { LucideIcon } from "lucide-react";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  BookOpen,
  CheckCircle2,
  ChevronRight,
  ClipboardList,
  Clock3,
  Database,
  FileText,
  LayoutDashboard,
  LogOut,
  Menu,
  Plus,
  RefreshCw,
  Search,
  Server,
  ShieldCheck,
  SlidersHorizontal,
  UserRound,
  Users,
  Wifi,
  X,
} from "lucide-react";
import { api } from "./api/client";
import type { components } from "./api/generated";
import { Badge } from "./components/ui/badge";
import { Alert, AlertDescription } from "./components/ui/alert";
import { Button } from "./components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "./components/ui/card";
import {
  Dialog as DialogRoot,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "./components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "./components/ui/alert-dialog";
import { Avatar, AvatarFallback } from "./components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./components/ui/dropdown-menu";
import { Input } from "./components/ui/input";
import { Label } from "./components/ui/label";
import {
  Select as ShadcnSelect,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./components/ui/table";
import { Textarea } from "./components/ui/textarea";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "./components/ui/sheet";
import { Separator } from "./components/ui/separator";
import { Skeleton } from "./components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "./components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "./components/ui/tooltip";
import { Toaster, toast } from "./components/ui/toast";
import { cn } from "./lib/utils";
import "./index.css";

type Exam = components["schemas"]["Exam"];
type Student = components["schemas"]["Student"];
type Session = components["schemas"]["Session"];
type Event = components["schemas"]["Event"];
type Section = "overview" | "exams" | "students" | "sessions" | "audit";

const navItems: { id: Section; label: string; icon: LucideIcon }[] = [
  { id: "overview", label: "总览", icon: LayoutDashboard },
  { id: "exams", label: "考试管理", icon: ClipboardList },
  { id: "students", label: "学生名单", icon: Users },
  { id: "sessions", label: "在线 Session", icon: Activity },
  { id: "audit", label: "审计日志", icon: FileText },
];

function formatDate(value?: string | number | null) {
  if (value === undefined || value === null || value === "") return "—";
  const date = new Date(typeof value === "number" ? value * 1000 : value);
  return Number.isNaN(date.getTime())
    ? "—"
    : date.toLocaleString("zh-CN", { dateStyle: "medium", timeStyle: "short" });
}
function stateLabel(state: string) {
  return (
    (
      {
        draft: "草稿",
        scheduled: "已排期",
        active: "进行中",
        ended: "已结束",
        authenticated: "已认证",
        suspended: "已暂停",
        created: "待认证",
        finished: "已结束",
      } as Record<string, string>
    )[state] || state
  );
}
function StateBadge({ state }: { state: string }) {
  const variant =
    state === "active" || state === "authenticated"
      ? "success"
      : state === "suspended"
        ? "destructive"
        : state === "scheduled"
          ? "warning"
          : "secondary";
  return <Badge variant={variant}>{stateLabel(state)}</Badge>;
}

function App() {
  const [token, setToken] = useState(
    () => localStorage.getItem("byod.admin_token") || "",
  );
  const [draftToken, setDraftToken] = useState(token);
  const [section, setSection] = useState<Section>("overview");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [exams, setExams] = useState<Exam[]>([]);
  const [students, setStudents] = useState<Student[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [events, setEvents] = useState<Event[]>([]);
  const [selectedExam, setSelectedExam] = useState<Exam | null>(null);
  const [selectedSession, setSelectedSession] = useState<Session | null>(null);
  const [sessionEvents, setSessionEvents] = useState<Event[]>([]);
  const [examDialogOpen, setExamDialogOpen] = useState(false);
  const [studentDialogOpen, setStudentDialogOpen] = useState(false);
  const [deleteExam, setDeleteExam] = useState<Exam | null>(null);
  const [editingExam, setEditingExam] = useState<Exam | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const loadExams = useCallback(async () => {
    const result = await api.GET("/admin/api/exams");
    if (result.error) {
      setError("无法加载考试数据，请确认管理员 token 和服务状态。");
      return;
    }
    setExams((result.data || []) as Exam[]);
  }, []);
  const loadSessions = useCallback(async () => {
    const result = await api.GET("/admin/api/sessions");
    if (result.error) {
      setError("无法加载在线 session。");
      return;
    }
    setSessions((result.data || []) as Session[]);
  }, []);
  const loadEvents = useCallback(async () => {
    const result = await api.GET("/admin/api/events");
    if (result.error) {
      setError("无法加载审计日志。");
      return;
    }
    setEvents((result.data || []) as Event[]);
  }, []);
  const refresh = useCallback(async () => {
    setBusy(true);
    setError("");
    await Promise.all([loadExams(), loadSessions(), loadEvents()]);
    setBusy(false);
  }, [loadEvents, loadExams, loadSessions]);
  useEffect(() => {
    if (token) void refresh();
  }, [refresh, token]);
  const loadStudents = useCallback(async (exam: Exam) => {
    setSelectedExam(exam);
    const result = await api.GET("/admin/api/exams/{examId}/students", {
      params: { path: { examId: exam.id } },
    });
    if (!result.error) setStudents((result.data || []) as Student[]);
  }, []);
  const openSection = (next: Section) => {
    setSection(next);
    setSidebarOpen(false);
    if (next === "sessions") void loadSessions();
    if (next === "audit") void loadEvents();
  };
  const logout = () => {
    localStorage.removeItem("byod.admin_token");
    setToken("");
    setDraftToken("");
  };
  const removeExam = async (exam: Exam) => {
    const result = await api.DELETE("/admin/api/exams/{examId}", {
      params: { path: { examId: exam.id } },
    });
    if (result.error) {
      setError("删除考试失败。");
      toast.add({ title: "删除失败", description: "考试未删除。", type: "error" });
      return;
    }
    toast.add({ title: "考试已删除", description: exam.id, type: "success" });
    if (selectedExam?.id === exam.id) setSelectedExam(null);
    await loadExams();
  };
  const openSession = async (session: Session) => {
    setSelectedSession(session);
    const result = await api.GET("/admin/api/sessions/{sessionId}/events", {
      params: { path: { sessionId: session.id } },
    });
    setSessionEvents(result.error ? [] : ((result.data || []) as Event[]));
  };
  const updateSession = async (action: "suspend" | "resume") => {
    if (!selectedSession) return;
    const result = await api.POST("/admin/api/sessions/{sessionId}", {
      params: { path: { sessionId: selectedSession.id } },
      body: { action },
    });
    if (result.error) {
      setError("更新 session 状态失败。");
      toast.add({ title: "Session 更新失败", type: "error" });
      return;
    }
    const next = result.data as Session;
    setSelectedSession(next);
    toast.add({ title: action === "suspend" ? "Session 已暂停" : "Session 已恢复", type: "success" });
    setSessions((items) =>
      items.map((item) => (item.id === next.id ? next : item)),
    );
  };
  if (!token)
    return (
      <LoginScreen
        draftToken={draftToken}
        setDraftToken={setDraftToken}
        onLogin={() => {
          const value = draftToken.trim();
          if (value) {
            localStorage.setItem("byod.admin_token", value);
            setToken(value);
          }
        }}
      />
    );
  const activeSessions = sessions.filter(
    (session) =>
      session.state === "active" || session.state === "authenticated",
  ).length;
  const activeExams = exams.filter((exam) => exam.state === "active").length;
  const currentTitle =
    navItems.find((item) => item.id === section)?.label || "总览";
  return (
    <div className="min-h-screen bg-slate-50">
      <aside className="fixed inset-y-0 left-0 z-40 hidden w-64 flex-col border-r border-slate-800 bg-slate-950 text-slate-300 lg:flex">
        <SidebarNav section={section} activeSessions={activeSessions} onNavigate={openSection} onLogout={logout} />
      </aside>
      <Sheet open={sidebarOpen} onOpenChange={setSidebarOpen}>
        <SheetContent side="left" className="w-72 border-slate-800 bg-slate-950 p-0 text-slate-300 sm:max-w-none">
          <SheetHeader className="sr-only">
            <SheetTitle>BYOD Server 导航</SheetTitle>
            <SheetDescription>考试控制中心导航</SheetDescription>
          </SheetHeader>
          <SidebarNav section={section} activeSessions={activeSessions} onNavigate={openSection} onLogout={logout} />
        </SheetContent>
      </Sheet>
      <div className="lg:pl-64">
        <header className="sticky top-0 z-30 flex h-16 items-center border-b border-slate-200 bg-white/90 px-4 backdrop-blur sm:px-8">
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon"
                  className="mr-3 lg:hidden"
                  onClick={() => setSidebarOpen(true)}
                />
              }
            >
              <Menu className="h-5 w-5" />
            </TooltipTrigger>
            <TooltipContent>打开导航</TooltipContent>
          </Tooltip>
          <div className="flex items-center gap-2 text-sm text-slate-400">
            <span>BYOD Server</span>
            <ChevronRight className="h-4 w-4" />
            <span className="font-medium text-slate-900">{currentTitle}</span>
          </div>
          <div className="ml-auto flex items-center gap-2">
            <span className="hidden items-center gap-1.5 text-xs text-emerald-600 sm:flex">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
              服务正常
            </span>
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void refresh()}
                    disabled={busy}
                  />
                }
              >
                <RefreshCw
                  className={cn("h-3.5 w-3.5", busy && "animate-spin")}
                />
                刷新
              </TooltipTrigger>
              <TooltipContent>重新加载考试、Session 和审计数据</TooltipContent>
            </Tooltip>
          </div>
        </header>
        <main className="mx-auto max-w-[1440px] space-y-6 p-4 sm:p-8">
          {error && (
            <Alert variant="destructive" className="flex items-center gap-3 px-4 py-3">
              <AlertTriangle className="h-4 w-4 shrink-0" />
              <AlertDescription className="text-sm text-red-700">{error}</AlertDescription>
              <Button variant="ghost" size="icon-xs" className="ml-auto text-red-700 hover:bg-red-100" onClick={() => setError("")}>
                <X className="h-4 w-4" />
                <span className="sr-only">关闭错误提示</span>
              </Button>
            </Alert>
          )}
          {section === "overview" && (
            <Overview
              exams={exams}
              sessions={sessions}
              events={events}
              activeExams={activeExams}
              activeSessions={activeSessions}
              loading={busy}
              onNavigate={openSection}
              onNewExam={() => {
                setEditingExam(null);
                setExamDialogOpen(true);
              }}
              onOpenExam={(exam) => {
                setEditingExam(exam);
                setExamDialogOpen(true);
              }}
            />
          )}
          {section === "exams" && (
            <ExamsPage
              exams={exams}
              selected={selectedExam}
              onNew={() => {
                setEditingExam(null);
                setExamDialogOpen(true);
              }}
              onEdit={(exam) => {
                setEditingExam(exam);
                setExamDialogOpen(true);
              }}
              onDelete={(exam) => setDeleteExam(exam)}
              onStudents={(exam) => {
                void loadStudents(exam);
                openSection("students");
              }}
            />
          )}
          {section === "students" && (
            <StudentsPage
              exams={exams}
              selected={selectedExam}
              students={students}
              onSelect={(exam) => void loadStudents(exam)}
              onAdd={() => setStudentDialogOpen(true)}
              onRefresh={() => selectedExam && void loadStudents(selectedExam)}
            />
          )}
          {section === "sessions" && (
            <SessionsPage
              sessions={sessions}
              onOpen={(session) => void openSession(session)}
              onRefresh={() => void loadSessions()}
            />
          )}
          {section === "audit" && (
            <AuditPage events={events} onRefresh={() => void loadEvents()} />
          )}
        </main>
      </div>
      <ExamDialog
        open={examDialogOpen}
        exam={editingExam}
        onClose={() => setExamDialogOpen(false)}
        onSaved={() => {
          setExamDialogOpen(false);
          void loadExams();
        }}
      />
      <StudentDialog
        open={studentDialogOpen}
        exam={selectedExam}
        onClose={() => setStudentDialogOpen(false)}
        onSaved={() => {
          setStudentDialogOpen(false);
          if (selectedExam) void loadStudents(selectedExam);
        }}
      />
      <SessionDialog
        session={selectedSession}
        events={sessionEvents}
        onClose={() => {
          setSelectedSession(null);
          setSessionEvents([]);
        }}
        onAction={(action) => void updateSession(action)}
      />
      <AlertDialog
        open={!!deleteExam}
        onOpenChange={(open) => !open && setDeleteExam(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除考试？</AlertDialogTitle>
            <AlertDialogDescription>
              {deleteExam
                ? `考试“${deleteExam.id}”及其名单、会话记录将被删除。此操作不可撤销。`
                : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setDeleteExam(null)}>
              取消
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (deleteExam) void removeExam(deleteExam);
                setDeleteExam(null);
              }}
            >
              删除考试
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function SidebarNav({
  section,
  activeSessions,
  onNavigate,
  onLogout,
}: {
  section: Section;
  activeSessions: number;
  onNavigate: (section: Section) => void;
  onLogout: () => void;
}) {
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-16 items-center gap-3 border-b border-slate-800 px-5">
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-500 text-white">
          <ShieldCheck className="h-5 w-5" />
        </div>
        <div>
          <p className="text-sm font-semibold tracking-wide text-white">
            BYOD SERVER
          </p>
          <p className="text-[11px] text-slate-500">考试控制中心</p>
        </div>
      </div>
      <div className="px-3 py-5">
        <p className="mb-2 px-3 text-[10px] font-semibold uppercase tracking-[.18em] text-slate-500">
          工作台
        </p>
        <nav className="space-y-1">
          {navItems.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              onClick={() => onNavigate(id)}
              className={cn(
                "flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-sm transition-colors",
                section === id
                  ? "bg-indigo-500/15 text-indigo-300"
                  : "text-slate-400 hover:bg-slate-900 hover:text-white",
              )}
            >
              <Icon className="h-4 w-4" />
              {label}
              {id === "sessions" && activeSessions > 0 && (
                <Badge className="ml-auto border-0 bg-emerald-400/15 text-emerald-300">
                  {activeSessions}
                </Badge>
              )}
            </button>
          ))}
        </nav>
      </div>
      <div className="mt-auto border-t border-slate-800 p-4">
        <DropdownMenu>
          <DropdownMenuTrigger
            className="mb-3 flex w-full items-center gap-3 rounded-lg bg-slate-900 p-3 text-left hover:bg-slate-800"
          >
            <Avatar size="sm" className="bg-slate-700 text-slate-200">
              <AvatarFallback className="bg-slate-700 text-slate-200">
                管
              </AvatarFallback>
            </Avatar>
            <div>
              <p className="text-xs font-medium text-slate-200">管理员</p>
              <p className="text-[11px] text-slate-500">Token session</p>
            </div>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-52">
            <DropdownMenuLabel>管理员账户</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={onLogout}>
              <LogOut className="h-4 w-4" />
              退出登录
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <Separator className="mb-3 bg-slate-800" />
        <p className="px-1 text-[11px] text-slate-500">BYOD Server · 管理端</p>
      </div>
    </div>
  );
}

function LoginScreen({
  draftToken,
  setDraftToken,
  onLogin,
}: {
  draftToken: string;
  setDraftToken: (value: string) => void;
  onLogin: () => void;
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-950 px-4">
      <div className="absolute inset-0 overflow-hidden">
        <div className="absolute -left-20 -top-40 h-96 w-96 rounded-full bg-indigo-600/20 blur-3xl" />
        <div className="absolute -bottom-40 -right-20 h-96 w-96 rounded-full bg-violet-600/10 blur-3xl" />
      </div>
      <Card className="relative w-full max-w-md border-slate-800 bg-slate-900 text-white shadow-2xl">
        <CardHeader className="space-y-4 pb-4">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-indigo-500">
            <ShieldCheck className="h-6 w-6" />
          </div>
          <div>
            <p className="text-xs font-semibold uppercase tracking-[.2em] text-indigo-300">
              BYOD SERVER
            </p>
            <CardTitle className="mt-2 text-2xl text-white">
              考试管理后台
            </CardTitle>
            <CardDescription className="mt-2 text-slate-400">
              使用管理员 token 访问控制中心
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              onLogin();
            }}
          >
            <Label className="text-slate-300">
              管理员 Token
              <Input
                autoFocus
                type="password"
                value={draftToken}
                onChange={(event) => setDraftToken(event.target.value)}
                placeholder="粘贴部署时生成的 token"
                className="mt-2 border-slate-700 bg-slate-950 text-white"
              />
            </Label>
            <Button className="h-10 w-full bg-indigo-500 hover:bg-indigo-400">
              进入控制中心
              <ArrowRight className="h-4 w-4" />
            </Button>
          </form>
          <p className="mt-5 text-center text-xs text-slate-500">
            Token 仅保存在当前浏览器的本地存储中
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
function MetricCard({
  title,
  value,
  detail,
  icon: Icon,
  tone,
}: {
  title: string;
  value: string;
  detail: string;
  icon: LucideIcon;
  tone: string;
}) {
  return (
    <Card>
      <CardContent className="flex items-start justify-between p-5">
        <div>
          <p className="text-sm text-slate-500">{title}</p>
          <p className="mt-2 text-3xl font-semibold tracking-tight text-slate-950">
            {value}
          </p>
          <p className="mt-1 text-xs text-slate-500">{detail}</p>
        </div>
        <div
          className={cn(
            "flex h-10 w-10 items-center justify-center rounded-lg",
            tone,
          )}
        >
          <Icon className="h-5 w-5" />
        </div>
      </CardContent>
    </Card>
  );
}
function Overview({
  exams,
  sessions,
  events,
  activeExams,
  activeSessions,
  loading,
  onNavigate,
  onNewExam,
  onOpenExam,
}: {
  exams: Exam[];
  sessions: Session[];
  events: Event[];
  activeExams: number;
  activeSessions: number;
  loading: boolean;
  onNavigate: (section: Section) => void;
  onNewExam: () => void;
  onOpenExam: (exam: Exam) => void;
}) {
  return (
    <>
      <PageHeading
        title="运行总览"
        description="实时掌握考试、设备和安全事件状态。"
        action={
          <Button onClick={onNewExam}>
            <Plus className="h-4 w-4" />
            新建考试
          </Button>
        }
      />
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {loading && !exams.length && !sessions.length && !events.length ? (
          Array.from({ length: 4 }).map((_, index) => (
            <Card key={index}>
              <CardContent className="space-y-3 p-5">
                <Skeleton className="h-4 w-24" />
                <Skeleton className="h-9 w-16" />
                <Skeleton className="h-3 w-32" />
              </CardContent>
            </Card>
          ))
        ) : (
          <>
        <MetricCard
          title="考试总数"
          value={String(exams.length)}
          detail={`${activeExams} 场正在进行`}
          icon={BookOpen}
          tone="bg-indigo-50 text-indigo-600"
        />
        <MetricCard
          title="在线 Session"
          value={String(activeSessions)}
          detail="当前活跃连接"
          icon={Wifi}
          tone="bg-emerald-50 text-emerald-600"
        />
        <MetricCard
          title="审计事件"
          value={String(events.length)}
          detail="最近记录总数"
          icon={FileText}
          tone="bg-amber-50 text-amber-600"
        />
        <MetricCard
          title="服务状态"
          value="正常"
          detail="数据库与隧道在线"
          icon={Server}
          tone="bg-sky-50 text-sky-600"
        />
          </>
        )}
      </div>
      <div className="grid gap-6 xl:grid-cols-[1.45fr_1fr]">
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <div>
              <CardTitle>最近考试</CardTitle>
              <CardDescription>源站、状态和考试时间</CardDescription>
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onNavigate("exams")}
            >
              查看全部
              <ArrowRight className="h-3.5 w-3.5" />
            </Button>
          </CardHeader>
          <CardContent className="pt-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>考试</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>开始时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {exams.slice(0, 5).map((exam) => (
                  <TableRow key={exam.id}>
                    <TableCell>
                      <button
                        className="text-left font-medium text-slate-900 hover:text-indigo-600"
                        onClick={() => onOpenExam(exam)}
                      >
                        {exam.id}
                      </button>
                      <p className="mt-0.5 max-w-xs truncate text-xs text-slate-500">
                        {exam.base_url}
                      </p>
                    </TableCell>
                    <TableCell>
                      <StateBadge state={exam.state} />
                    </TableCell>
                    <TableCell className="text-slate-500">
                      {formatDate(exam.starts_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onOpenExam(exam)}
                      >
                        编辑
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
                {!exams.length && (
                  <TableRow>
                    <TableCell
                      colSpan={4}
                      className="py-12 text-center text-slate-500"
                    >
                      暂无考试，点击右上角创建第一场考试。
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>系统状态</CardTitle>
            <CardDescription>关键组件健康检查</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4 pt-0">
            <HealthRow
              icon={CheckCircle2}
              label="BYOD Server API"
              detail="HTTP API 正常"
            />
            <HealthRow icon={Database} label="PostgreSQL" detail="迁移已完成" />
            <HealthRow
              icon={Wifi}
              label="透明 Tunnel"
              detail={`${sessions.length} 个 session 记录`}
            />
            <HealthRow
              icon={ShieldCheck}
              label="策略签名"
              detail="HMAC 校验启用"
            />
          </CardContent>
        </Card>
      </div>
    </>
  );
}
function HealthRow({
  icon: Icon,
  label,
  detail,
}: {
  icon: LucideIcon;
  label: string;
  detail: string;
}) {
  return (
    <div className="flex items-center gap-3">
      <div className="flex h-8 w-8 items-center justify-center rounded-full bg-emerald-50 text-emerald-600">
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <p className="text-sm font-medium text-slate-800">{label}</p>
        <p className="text-xs text-slate-500">{detail}</p>
      </div>
      <span className="ml-auto h-2 w-2 rounded-full bg-emerald-500" />
    </div>
  );
}
function PageHeading({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-end">
      <div>
        <h1 className="text-3xl font-semibold tracking-tight text-slate-950">
          {title}
        </h1>
        <p className="mt-2 text-sm text-slate-500">{description}</p>
      </div>
      {action}
    </div>
  );
}
function SelectField({
  value,
  onValueChange,
  options,
  placeholder,
  className,
}: {
  value: string;
  onValueChange: (value: string) => void;
  options: { value: string; label: string }[];
  placeholder?: string;
  className?: string;
}) {
  return (
    <ShadcnSelect
      value={value}
      onValueChange={(next) => {
        if (next) onValueChange(next);
      }}
    >
      <SelectTrigger className={cn("w-full", className)}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </ShadcnSelect>
  );
}

function ExamsPage({
  exams,
  selected,
  onNew,
  onEdit,
  onDelete,
  onStudents,
}: {
  exams: Exam[];
  selected: Exam | null;
  onNew: () => void;
  onEdit: (exam: Exam) => void;
  onDelete: (exam: Exam) => void;
  onStudents: (exam: Exam) => void;
}) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const filtered = exams.filter(
    (exam) =>
      (!query ||
        `${exam.id} ${exam.base_url}`
          .toLowerCase()
          .includes(query.toLowerCase())) &&
      (filter === "all" || exam.state === filter),
  );
  return (
    <>
      <PageHeading
        title="考试管理"
        description="创建考试、配置源站和发布浏览器策略。"
        action={
          <Button onClick={onNew}>
            <Plus className="h-4 w-4" />
            新建考试
          </Button>
        }
      />
      <Card>
        <CardHeader className="gap-4 border-b border-slate-100 pb-4 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>
              全部考试{" "}
              <span className="ml-1 text-sm font-normal text-slate-400">
                {exams.length}
              </span>
            </CardTitle>
            <CardDescription>
              源站地址和策略均持久化在 BYOD 数据库。
            </CardDescription>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row">
            <div className="relative">
              <Search className="absolute left-3 top-2.5 h-4 w-4 text-slate-400" />
              <Input
                className="w-full pl-9 sm:w-56"
                placeholder="搜索考试…"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </div>
            <div className="relative">
              <SlidersHorizontal className="pointer-events-none absolute left-3 top-2 h-4 w-4 text-slate-400" />
              <SelectField
                className="pl-8 sm:w-36"
                value={filter}
                onValueChange={setFilter}
                options={[
                  { value: "all", label: "全部状态" },
                  { value: "draft", label: "草稿" },
                  { value: "scheduled", label: "已排期" },
                  { value: "active", label: "进行中" },
                  { value: "ended", label: "已结束" },
                ]}
              />
            </div>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>考试</TableHead>
                <TableHead>源站 Base URL</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((exam) => (
                <TableRow
                  key={exam.id}
                  className={selected?.id === exam.id ? "bg-indigo-50/40" : ""}
                >
                  <TableCell>
                    <p className="font-medium text-slate-900">{exam.id}</p>
                    <p className="text-xs text-slate-500">
                      grips://exam.cs.ac.cn/{exam.id}
                    </p>
                  </TableCell>
                  <TableCell className="max-w-xs truncate text-slate-600">
                    {exam.base_url}
                  </TableCell>
                  <TableCell>
                    <StateBadge state={exam.state} />
                  </TableCell>
                  <TableCell className="text-xs text-slate-500">
                    <div>{formatDate(exam.starts_at)}</div>
                    {exam.ends_at && <div>至 {formatDate(exam.ends_at)}</div>}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onStudents(exam)}
                      >
                        学生
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onEdit(exam)}
                      >
                        编辑
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="text-red-600 hover:bg-red-50 hover:text-red-700"
                        onClick={() => onDelete(exam)}
                      >
                        删除
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {!filtered.length && (
                <TableRow>
                  <TableCell
                    colSpan={5}
                    className="py-14 text-center text-slate-500"
                  >
                    {exams.length
                      ? "没有匹配的考试"
                      : "暂无考试，点击右上角新建。"}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </>
  );
}

function StudentsPage({
  exams,
  selected,
  students,
  onSelect,
  onAdd,
  onRefresh,
}: {
  exams: Exam[];
  selected: Exam | null;
  students: Student[];
  onSelect: (exam: Exam) => void;
  onAdd: () => void;
  onRefresh: () => void;
}) {
  return (
    <>
      <PageHeading
        title="学生名单"
        description="管理每场考试的允许参加人员和访问权限。"
        action={
          selected ? (
            <Button onClick={onAdd}>
              <Plus className="h-4 w-4" />
              添加学生
            </Button>
          ) : undefined
        }
      />
      <Card>
        <CardHeader className="border-b border-slate-100 pb-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <CardTitle>
                {selected ? `${selected.id} · 参加名单` : "选择一场考试"}
              </CardTitle>
              <CardDescription>
                {selected
                  ? "只有启用的 subject 可以通过 OIDC 认证后进入考试。"
                  : "请选择考试以查看和编辑学生名单。"}
              </CardDescription>
            </div>
            {exams.length > 0 && (
              <SelectField
                className="sm:w-64"
                value={selected?.id || ""}
                placeholder="选择考试…"
                onValueChange={(value) => {
                  const exam = exams.find((item) => item.id === value);
                  if (exam) onSelect(exam);
                }}
                options={exams.map((exam) => ({
                  value: exam.id,
                  label: exam.id,
                }))}
              />
            )}
          </div>
        </CardHeader>
        {selected ? (
          <CardContent className="p-0">
            <div className="flex items-center justify-between border-b border-slate-100 bg-slate-50/60 px-6 py-3 text-xs text-slate-500">
              <span>
                {students.length
                  ? `已配置 ${students.length} 名学生`
                  : "尚未配置白名单（默认允许所有已认证用户）"}
              </span>
              <Button variant="ghost" size="sm" onClick={onRefresh}>
                <RefreshCw className="h-3.5 w-3.5" />
                刷新
              </Button>
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>学生</TableHead>
                  <TableHead>OIDC Subject</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {students.map((student) => (
                  <StudentRow
                    key={student.subject}
                    student={student}
                    examId={selected.id}
                    onChanged={onRefresh}
                  />
                ))}
                {!students.length && (
                  <TableRow>
                    <TableCell
                      colSpan={4}
                      className="py-14 text-center text-slate-500"
                    >
                      <Users className="mx-auto mb-3 h-8 w-8 text-slate-300" />
                      暂无学生名单
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </CardContent>
        ) : (
          <CardContent className="py-16 text-center text-slate-500">
            <ClipboardList className="mx-auto mb-3 h-9 w-9 text-slate-300" />
            请先选择一场考试
          </CardContent>
        )}
      </Card>
    </>
  );
}
function StudentRow({
  student,
  examId,
  onChanged,
}: {
  student: Student;
  examId: string;
  onChanged: () => void;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const toggle = async () => {
    const result = await api.PUT("/admin/api/exams/{examId}/students/{subject}", {
      params: { path: { examId, subject: student.subject } },
      body: { display_name: student.display_name, enabled: !student.enabled },
    });
    if (result.error) {
      toast.add({ title: "更新学生失败", description: student.subject, type: "error" });
      return;
    }
    toast.add({ title: student.enabled ? "学生已禁用" : "学生已启用", type: "success" });
    onChanged();
  };
  const remove = async () => {
    const result = await api.DELETE("/admin/api/exams/{examId}/students/{subject}", {
      params: { path: { examId, subject: student.subject } },
    });
    if (result.error) {
      toast.add({ title: "移除学生失败", type: "error" });
      return;
    }
    toast.add({ title: "学生已移除", description: student.subject, type: "success" });
    onChanged();
  };
  return (
    <>
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-indigo-50 text-indigo-600">
            <UserRound className="h-4 w-4" />
          </div>
          <span className="font-medium">
            {student.display_name || "未命名学生"}
          </span>
        </div>
      </TableCell>
      <TableCell className="font-mono text-xs text-slate-500">
        {student.subject}
      </TableCell>
      <TableCell>
        <Badge variant={student.enabled ? "success" : "secondary"}>
          {student.enabled ? "已启用" : "已禁用"}
        </Badge>
      </TableCell>
      <TableCell>
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="sm" onClick={() => void toggle()}>
            {student.enabled ? "禁用" : "启用"}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-red-600 hover:bg-red-50"
            onClick={() => setConfirmOpen(true)}
          >
            移除
          </Button>
        </div>
      </TableCell>
    </TableRow>
    <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>移除学生？</AlertDialogTitle>
          <AlertDialogDescription>
            将从当前考试名单移除 {student.subject}，之后该账号不能再参加此考试。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={() => void remove()}>
            移除学生
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
    </>
  );
}
function SessionsPage({
  sessions,
  onOpen,
  onRefresh,
}: {
  sessions: Session[];
  onOpen: (session: Session) => void;
  onRefresh: () => void;
}) {
  const [view, setView] = useState("all");
  const visibleSessions = sessions.filter((session) =>
    view === "all" ? true : view === "active" ? session.state === "active" || session.state === "authenticated" : session.state === "suspended",
  );
  return (
    <>
      <PageHeading
        title="在线 Session"
        description="实时查看学生作答连接、设备状态和违规计数。"
        action={
          <Button variant="outline" onClick={onRefresh}>
            <RefreshCw className="h-4 w-4" />
            刷新列表
          </Button>
        }
      />
      <Card>
        <CardHeader className="border-b border-slate-100 pb-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <CardTitle>
                作答连接{" "}
                <span className="ml-1 text-sm font-normal text-slate-400">
                  {visibleSessions.length}
                </span>
              </CardTitle>
              <CardDescription>
                点击任意行查看事件时间线并暂停或恢复 session。
              </CardDescription>
            </div>
            <Tabs value={view} onValueChange={(value) => setView(String(value))}>
              <TabsList>
                <TabsTrigger value="all">全部</TabsTrigger>
                <TabsTrigger value="active">活跃</TabsTrigger>
                <TabsTrigger value="suspended">已暂停</TabsTrigger>
              </TabsList>
            </Tabs>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Session ID</TableHead>
                <TableHead>考试</TableHead>
                <TableHead>Subject</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>最后心跳</TableHead>
                <TableHead>违规</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleSessions.map((session) => (
                <TableRow
                  key={session.id}
                  className="cursor-pointer"
                  onClick={() => onOpen(session)}
                >
                  <TableCell className="font-mono text-xs text-slate-600">
                    {session.id.slice(0, 18)}…
                  </TableCell>
                  <TableCell className="font-medium">
                    {session.exam_id}
                  </TableCell>
                  <TableCell className="max-w-xs truncate text-slate-500">
                    {session.subject || "—"}
                  </TableCell>
                  <TableCell>
                    <StateBadge state={session.state} />
                  </TableCell>
                  <TableCell className="text-xs text-slate-500">
                    {formatDate(session.last_seen_at)}
                  </TableCell>
                  <TableCell>
                    {session.violation_count ? (
                      <span className="font-medium text-red-600">
                        {session.violation_count}
                      </span>
                    ) : (
                      <span className="text-slate-400">0</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {!visibleSessions.length && (
                <TableRow>
                  <TableCell
                    colSpan={6}
                    className="py-14 text-center text-slate-500"
                  >
                    <Wifi className="mx-auto mb-3 h-8 w-8 text-slate-300" />
                    暂无在线 session
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </>
  );
}
function AuditPage({
  events,
  onRefresh,
}: {
  events: Event[];
  onRefresh: () => void;
}) {
  return (
    <>
      <PageHeading
        title="审计日志"
        description="追踪认证、策略违规和会话状态变更。"
        action={
          <Button variant="outline" onClick={onRefresh}>
            <RefreshCw className="h-4 w-4" />
            刷新日志
          </Button>
        }
      />
      <Card>
        <CardHeader className="border-b border-slate-100 pb-4">
          <CardTitle>最近事件</CardTitle>
          <CardDescription>
            按发生时间倒序显示最近 {events.length || 0} 条事件。
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-slate-100">
            {events.map((event) => (
              <div key={event.id} className="flex gap-4 px-6 py-4">
                <div
                  className={cn(
                    "mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full",
                    event.severity === "critical" || event.severity === "high"
                      ? "bg-red-50 text-red-600"
                      : "bg-slate-100 text-slate-500",
                  )}
                >
                  <AlertTriangle className="h-4 w-4" />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <p className="font-medium text-slate-900">{event.type}</p>
                    <Badge
                      variant={
                        event.severity === "critical" ||
                        event.severity === "high"
                          ? "destructive"
                          : "secondary"
                      }
                    >
                      {event.severity}
                    </Badge>
                  </div>
                  <p className="mt-1 text-xs text-slate-500">
                    {event.session_id}{" "}
                    {event.details ? ` · ${event.details}` : ""}
                  </p>
                </div>
                <time className="shrink-0 text-xs text-slate-400">
                  {formatDate(event.occurred_at)}
                </time>
              </div>
            ))}
            {!events.length && (
              <div className="py-14 text-center text-slate-500">
                <FileText className="mx-auto mb-3 h-8 w-8 text-slate-300" />
                暂无审计事件
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </>
  );
}

function AppDialog({
  open,
  onClose,
  title,
  description,
  className,
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <DialogRoot open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className={cn("max-h-[90vh] overflow-y-auto sm:max-w-xl", className)}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {children}
      </DialogContent>
    </DialogRoot>
  );
}

function ExamDialog({
  open,
  exam,
  onClose,
  onSaved,
}: {
  open: boolean;
  exam: Exam | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [id, setId] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [state, setState] = useState<Exam["state"]>("draft");
  const [starts, setStarts] = useState("");
  const [ends, setEnds] = useState("");
  const [policy, setPolicy] = useState("{}");
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!open) return;
    setId(exam?.id || "");
    setBaseURL(exam?.base_url || "");
    setState(exam?.state || "draft");
    setStarts(exam?.starts_at ? exam.starts_at.slice(0, 16) : "");
    setEnds(exam?.ends_at ? exam.ends_at.slice(0, 16) : "");
    setPolicy(exam?.policy ? JSON.stringify(exam.policy, null, 2) : "{}");
    setFormError("");
  }, [exam, open]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setFormError("");
    let policyValue: Record<string, unknown>;
    try {
      policyValue = JSON.parse(policy);
    } catch {
      setFormError("策略必须是合法 JSON。");
      return;
    }
    if (!id.trim() || !baseURL.trim()) {
      setFormError("考试 ID 和源站 URL 不能为空。");
      return;
    }
    setSaving(true);
    const body = {
      id: id.trim(),
      base_url: baseURL.trim(),
      state,
      starts_at: starts ? new Date(starts).toISOString() : null,
      ends_at: ends ? new Date(ends).toISOString() : null,
      policy: policyValue,
    };
    const result = exam
      ? await api.PATCH("/admin/api/exams/{examId}", {
          params: { path: { examId: exam.id } },
          body,
        })
      : await api.POST("/admin/api/exams", { body });
    setSaving(false);
    if (result.error) {
      setFormError("保存失败，请检查 ID、URL 和服务日志。");
      toast.add({ title: "保存考试失败", type: "error" });
      return;
    }
    toast.add({ title: exam ? "考试已更新" : "考试已创建", description: id.trim(), type: "success" });
    onSaved();
  };
  return (
    <AppDialog
      open={open}
      onClose={onClose}
      title={exam ? "编辑考试" : "新建考试"}
      description="配置考试源站、开放时间和浏览器策略"
    >
      <form className="space-y-4" onSubmit={(event) => void submit(event)}>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="exam-id">考试 ID</Label>
            <Input
              id="exam-id"
              value={id}
              disabled={!!exam}
              onChange={(event) => setId(event.target.value)}
              placeholder="course-101"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="exam-state">状态</Label>
            <SelectField
              value={state}
              onValueChange={(value) => setState(value as Exam["state"])}
              options={[
                { value: "draft", label: "草稿" },
                { value: "scheduled", label: "已排期" },
                { value: "active", label: "进行中" },
                { value: "ended", label: "已结束" },
              ]}
            />
          </div>
        </div>
        <div className="space-y-2">
          <Label htmlFor="exam-base">源站 Base URL</Label>
          <Input
            id="exam-base"
            type="url"
            value={baseURL}
            onChange={(event) => setBaseURL(event.target.value)}
            placeholder="https://cs101.gbu.edu.cn"
          />
          <p className="text-xs text-slate-500">
            必须是 http(s) URL；考试页面的 HTTPS 请求将通过透明 tunnel 回源。
          </p>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="exam-start">开始时间（可选）</Label>
            <Input
              id="exam-start"
              type="datetime-local"
              value={starts}
              onChange={(event) => setStarts(event.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="exam-end">结束时间（可选）</Label>
            <Input
              id="exam-end"
              type="datetime-local"
              value={ends}
              onChange={(event) => setEnds(event.target.value)}
            />
          </div>
        </div>
        <div className="space-y-2">
          <Label htmlFor="exam-policy">浏览器策略 JSON</Label>
          <Textarea
            id="exam-policy"
            className="min-h-40 font-mono text-xs"
            value={policy}
            onChange={(event) => setPolicy(event.target.value)}
            spellCheck={false}
          />
          <p className="text-xs text-slate-500">
            策略会在签名后下发给 BYOD Browser；空对象使用服务端安全基线。
          </p>
        </div>
        {formError && (
          <p className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={saving}>
            {saving && <RefreshCw className="h-4 w-4 animate-spin" />}
            {saving ? "保存中…" : "保存考试"}
          </Button>
        </div>
      </form>
    </AppDialog>
  );
}
function StudentDialog({
  open,
  exam,
  onClose,
  onSaved,
}: {
  open: boolean;
  exam: Exam | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [subject, setSubject] = useState("");
  const [name, setName] = useState("");
  const [saving, setSaving] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!exam || !subject.trim()) return;
    setSaving(true);
    const result = await api.PUT(
      "/admin/api/exams/{examId}/students/{subject}",
      {
        params: { path: { examId: exam.id, subject: subject.trim() } },
        body: { display_name: name.trim(), enabled: true },
      },
    );
    setSaving(false);
    if (!result.error) {
      toast.add({ title: "学生已添加", description: subject.trim(), type: "success" });
      setSubject("");
      setName("");
      onSaved();
    } else {
      toast.add({ title: "添加学生失败", type: "error" });
    }
  };
  return (
    <AppDialog
      open={open}
      onClose={onClose}
      title="添加学生"
      description={
        exam ? `将学生加入 ${exam.id} 的参加名单。` : "请先选择考试。"
      }
    >
      <form className="space-y-4" onSubmit={(event) => void submit(event)}>
        <div className="space-y-2">
          <Label htmlFor="student-subject">OIDC Subject</Label>
          <Input
            id="student-subject"
            value={subject}
            onChange={(event) => setSubject(event.target.value)}
            placeholder="填写 ID Token 的 sub claim"
          />
          <p className="text-xs text-slate-500">
            必须填写 OIDC ID Token 中的 sub 原值，不是昵称。
          </p>
        </div>
        <div className="space-y-2">
          <Label htmlFor="student-name">显示名称（可选）</Label>
          <Input
            id="student-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="张三"
          />
        </div>
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={saving || !subject.trim()}>
            {saving ? "添加中…" : "添加学生"}
          </Button>
        </div>
      </form>
    </AppDialog>
  );
}
function SessionDialog({
  session,
  events,
  onClose,
  onAction,
}: {
  session: Session | null;
  events: Event[];
  onClose: () => void;
  onAction: (action: "suspend" | "resume") => void;
}) {
  return (
    <AppDialog
      open={!!session}
      onClose={onClose}
      title="Session 详情"
      description={session ? `${session.exam_id} · ${session.id}` : undefined}
      className="max-w-2xl"
    >
      {session && (
        <div className="space-y-5">
          <div className="grid grid-cols-2 gap-4 rounded-lg bg-slate-50 p-4 sm:grid-cols-4">
            <Detail label="状态">
              <StateBadge state={session.state} />
            </Detail>
            <Detail label="Subject">
              <span className="break-all font-mono text-xs">
                {session.subject || "—"}
              </span>
            </Detail>
            <Detail label="违规次数">
              <span
                className={
                  session.violation_count
                    ? "font-semibold text-red-600"
                    : "font-semibold"
                }
              >
                {session.violation_count || 0}
              </span>
            </Detail>
            <Detail label="最后心跳">
              <span className="text-xs">
                {formatDate(session.last_seen_at)}
              </span>
            </Detail>
          </div>
          <div>
            <h3 className="mb-2 text-sm font-semibold">事件时间线</h3>
            <div className="max-h-64 overflow-y-auto rounded-lg border border-slate-200">
              {events.map((event) => (
                <div
                  key={event.id}
                  className="flex gap-3 border-b border-slate-100 px-4 py-3 last:border-0"
                >
                  <Clock3 className="mt-0.5 h-4 w-4 shrink-0 text-slate-400" />
                  <div className="min-w-0">
                    <p className="text-sm font-medium">
                      {event.type}{" "}
                      <span className="ml-1 text-xs font-normal text-slate-400">
                        {event.severity}
                      </span>
                    </p>
                    <p className="text-xs text-slate-500">
                      {event.details || "无附加信息"}
                    </p>
                    <p className="mt-1 text-[11px] text-slate-400">
                      {formatDate(event.occurred_at)}
                    </p>
                  </div>
                </div>
              ))}
              {!events.length && (
                <p className="px-4 py-8 text-center text-sm text-slate-500">
                  暂无事件
                </p>
              )}
            </div>
          </div>
          <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
            {session.state === "active" && (
              <Button
                variant="outline"
                className="text-amber-700"
                onClick={() => onAction("suspend")}
              >
                <AlertTriangle className="h-4 w-4" />
                暂停 Session
              </Button>
            )}
            {session.state === "suspended" && (
              <Button
                variant="outline"
                className="text-emerald-700"
                onClick={() => onAction("resume")}
              >
                <CheckCircle2 className="h-4 w-4" />
                恢复 Session
              </Button>
            )}
            <Button onClick={onClose}>关闭</Button>
          </div>
        </div>
      )}
    </AppDialog>
  );
}
function Detail({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <p className="mb-1 text-[11px] text-slate-500">{label}</p>
      {children}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TooltipProvider delay={200}>
      <Toaster>
        <App />
      </Toaster>
    </TooltipProvider>
  </StrictMode>,
);
