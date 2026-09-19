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
import { Switch } from "./components/ui/switch";
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
type ExamAdmin = components["schemas"]["ExamAdmin"];
type Section = "overview" | "exams" | "users" | "students" | "sessions" | "audit";

/**
 * The admin UI deliberately uses the browser's History API instead of adding
 * another router dependency.  The server serves index.html for every
 * /admin/* path, so these URLs are also safe to bookmark and reload.
 */
type AdminRoute = {
  section: Section;
  examId?: string;
  sessionId?: string;
  modal?: "new-exam" | "edit-exam" | "add-student" | "session";
};

function parseRoute(pathname: string): AdminRoute {
  const path = pathname.replace(/\/+$/, "").replace(/^\/admin\/?/, "");
  const parts = path
    .split("/")
    .filter(Boolean)
    .map((part) => {
      try {
        return decodeURIComponent(part);
      } catch {
        return part;
      }
    });

  if (!parts.length) return { section: "overview" };
  if (parts[0] === "exams") {
    if (parts[1] === "new") return { section: "exams", modal: "new-exam" };
    if (parts[1]) {
      if (parts[2] === "edit") {
        return { section: "exams", examId: parts[1], modal: "edit-exam" };
      }
      if (parts[2] === "participants") {
        return {
          section: "students",
          examId: parts[1],
          modal: parts[3] === "new" ? "add-student" : undefined,
        };
      }
      return { section: "exams", examId: parts[1] };
    }
    return { section: "exams" };
  }
  if (parts[0] === "users") return { section: "users" };
  if (parts[0] === "students") {
    return parts[1]
      ? { section: "students", examId: parts[1] }
      : { section: "students" };
  }
  if (parts[0] === "sessions") {
    return parts[1]
      ? { section: "sessions", sessionId: parts[1], modal: "session" }
      : { section: "sessions" };
  }
  if (parts[0] === "audit") return { section: "audit" };
  return { section: "overview" };
}

function routePath(route: AdminRoute): string {
  const encode = (value: string) => encodeURIComponent(value);
  if (route.section === "overview") return "/admin/";
  if (route.modal === "new-exam") return "/admin/exams/new";
  if (route.section === "exams") {
    if (route.examId && route.modal === "edit-exam") {
      return `/admin/exams/${encode(route.examId)}/edit`;
    }
    if (route.examId) return `/admin/exams/${encode(route.examId)}`;
    return "/admin/exams";
  }
  if (route.section === "students") {
    if (route.examId && route.modal === "add-student") {
      return `/admin/exams/${encode(route.examId)}/participants/new`;
    }
    if (route.examId) return `/admin/exams/${encode(route.examId)}/participants`;
    return "/admin/students";
  }
  if (route.section === "sessions") {
    if (route.sessionId) return `/admin/sessions/${encode(route.sessionId)}`;
    return "/admin/sessions";
  }
  return `/admin/${route.section}`;
}

const navItems: { id: Section; label: string; icon: LucideIcon }[] = [
  { id: "overview", label: "总览", icon: LayoutDashboard },
  { id: "exams", label: "考试管理", icon: ClipboardList },
  { id: "users", label: "用户管理", icon: Users },
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
  const [user, setUser] = useState<components["schemas"]["User"] | null>(null);
  const [authLoading, setAuthLoading] = useState(true);
  const [route, setRoute] = useState<AdminRoute>(() =>
    parseRoute(window.location.pathname),
  );
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [exams, setExams] = useState<Exam[]>([]);
  const [students, setStudents] = useState<Student[]>([]);
  const [examAdmins, setExamAdmins] = useState<ExamAdmin[]>([]);
  const [users, setUsers] = useState<components["schemas"]["User"][]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [events, setEvents] = useState<Event[]>([]);
  const [selectedExam, setSelectedExam] = useState<Exam | null>(null);
  const [selectedSession, setSelectedSession] = useState<Session | null>(null);
  const [sessionEvents, setSessionEvents] = useState<Event[]>([]);
  const [deleteExam, setDeleteExam] = useState<Exam | null>(null);
  const [editingExam, setEditingExam] = useState<Exam | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const section = route.section;
  const examDialogOpen =
    route.modal === "new-exam" ||
    (route.modal === "edit-exam" && editingExam !== null);
  const studentDialogOpen = route.modal === "add-student" && selectedExam !== null;

  const navigate = useCallback((next: AdminRoute, replace = false) => {
    const nextPath = routePath(next);
    if (window.location.pathname !== nextPath) {
      window.history[replace ? "replaceState" : "pushState"]({}, "", nextPath);
    }
    setRoute(next);
    setSidebarOpen(false);
  }, []);

  useEffect(() => {
    const onPopState = () => setRoute(parseRoute(window.location.pathname));
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

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
  const loadUsers = useCallback(async () => {
    const result = await api.GET("/admin/api/users");
    if (!result.error) setUsers((result.data || []) as components["schemas"]["User"][]);
  }, []);
  const refresh = useCallback(async () => {
    setBusy(true);
    setError("");
    await Promise.all([loadExams(), loadSessions(), loadEvents(), loadUsers()]);
    setBusy(false);
  }, [loadEvents, loadExams, loadSessions, loadUsers]);
  useEffect(() => {
    void api.GET("/auth/me").then((result) => {
      if (!result.error && (result.data?.capabilities.platform_admin || result.data?.capabilities.exam_admin)) {
        localStorage.setItem("byod.csrf_token", result.data.csrf_token);
        setUser(result.data.user as components["schemas"]["User"]);
        void refresh();
      }
      setAuthLoading(false);
    });
  }, [refresh]);
  const loadStudents = useCallback(async (exam: Exam) => {
    setSelectedExam(exam);
    const [result, adminsResult] = await Promise.all([api.GET("/admin/api/exams/{examId}/participants", {
      params: { path: { examId: exam.id } },
    }), api.GET("/admin/api/exams/{examId}/admins", { params: { path: { examId: exam.id } } })]);
    if (!result.error) setStudents(((result.data || []) as components["schemas"]["Participant"][]).map((item) => ({subject: item.user.id, display_name: item.user.email || item.user.display_name, enabled: item.enabled})));
    if (!adminsResult.error) setExamAdmins((adminsResult.data || []) as ExamAdmin[]);
  }, []);
  const openSession = useCallback(async (session: Session) => {
    setSelectedSession(session);
    const result = await api.GET("/admin/api/sessions/{sessionId}/events", {
      params: { path: { sessionId: session.id } },
    });
    setSessionEvents(result.error ? [] : ((result.data || []) as Event[]));
  }, []);
  useEffect(() => {
    const exam = route.examId
      ? exams.find((item) => item.id === route.examId)
      : undefined;
    if (exam) {
      setSelectedExam(exam);
      if (route.modal === "edit-exam") setEditingExam(exam);
      if (route.section === "students") void loadStudents(exam);
    } else if (route.modal === "new-exam") {
      setSelectedExam(null);
      setEditingExam(null);
    } else if (route.section !== "students" || !route.examId) {
      setSelectedExam(null);
    }
    if (route.modal !== "edit-exam" && route.modal !== "new-exam") {
      setEditingExam(null);
    }
  }, [exams, loadStudents, route]);

  useEffect(() => {
    if (route.modal === "session" && route.sessionId) {
      const session = sessions.find((item) => item.id === route.sessionId);
      if (session) void openSession(session);
      return;
    }
    if (route.section !== "sessions") {
      setSelectedSession(null);
      setSessionEvents([]);
    }
  }, [openSession, route, sessions]);

  const openSection = (next: Section) => {
    navigate({ section: next });
    if (next === "sessions") void loadSessions();
    if (next === "audit") void loadEvents();
  };
  const logout = () => {
    void api.POST("/auth/logout", { body: undefined }).finally(() => {
      localStorage.removeItem("byod.csrf_token");
      setUser(null);
    });
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
  const publishExam = async (exam: Exam) => {
    const result = await api.POST("/admin/api/exams/{examId}/publish", {
      params: { path: { examId: exam.id } },
    });
    if (result.error) {
      setError("发布考试失败，请检查开始/结束时间。 ");
      toast.add({ title: "发布失败", type: "error" });
      return;
    }
    toast.add({ title: "考试已发布", description: exam.id, type: "success" });
    await loadExams();
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
  if (authLoading) return <div className="flex min-h-screen items-center justify-center bg-slate-950 text-white">正在验证 Connect 登录…</div>;
  if (!user)
    return (
      <LoginScreen
        onLogin={() => {
          window.location.href = `/auth/login?return_to=${encodeURIComponent(window.location.pathname)}`;
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
              onNewExam={() => navigate({ section: "exams", modal: "new-exam" })}
              onOpenExam={(exam) =>
                navigate({ section: "exams", examId: exam.id, modal: "edit-exam" })
              }
            />
          )}
          {section === "exams" && (
            <ExamsPage
              exams={exams}
              selected={selectedExam}
              onNew={() => navigate({ section: "exams", modal: "new-exam" })}
              onEdit={(exam) =>
                navigate({ section: "exams", examId: exam.id, modal: "edit-exam" })
              }
              onDelete={(exam) => setDeleteExam(exam)}
              onPublish={(exam) => void publishExam(exam)}
              onStudents={(exam) =>
                navigate({ section: "students", examId: exam.id })
              }
            />
          )}
          {section === "users" && <UsersPage users={users} onRefresh={() => void loadUsers()} />}
          {section === "students" && (
              <StudentsPage
              exams={exams}
              selected={selectedExam}
              students={students}
              examAdmins={examAdmins}
              users={users}
              onSelect={(exam) =>
                navigate({ section: "students", examId: exam.id })
              }
              onAdd={() =>
                selectedExam &&
                navigate({
                  section: "students",
                  examId: selectedExam.id,
                  modal: "add-student",
                })
              }
              onRefresh={() => selectedExam && void loadStudents(selectedExam)}
            />
          )}
          {section === "sessions" && (
            <SessionsPage
              sessions={sessions}
              onOpen={(session) =>
                navigate({ section: "sessions", sessionId: session.id, modal: "session" })
              }
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
        onClose={() => navigate({ section: "exams" })}
        onSaved={() => {
          navigate({ section: "exams" });
          void loadExams();
        }}
      />
      <StudentDialog
        open={studentDialogOpen}
        exam={selectedExam}
        users={users}
        onClose={() =>
          selectedExam
            ? navigate({ section: "students", examId: selectedExam.id })
            : navigate({ section: "students" })
        }
        onSaved={() => {
          if (selectedExam) {
            navigate({ section: "students", examId: selectedExam.id });
          } else {
            navigate({ section: "students" });
          }
          if (selectedExam) void loadStudents(selectedExam);
        }}
      />
      <SessionDialog
        session={selectedSession}
        events={sessionEvents}
        onClose={() => {
          setSelectedSession(null);
          setSessionEvents([]);
          navigate({ section: "sessions" });
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

function LoginScreen({ onLogin }: { onLogin: () => void }) {
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
              使用 Connect OIDC 登录；只有管理员角色可以进入控制中心
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent>
          <Button type="button" className="h-10 w-full bg-indigo-500 hover:bg-indigo-400" onClick={onLogin}>
            使用 Connect 登录 <ArrowRight className="h-4 w-4" />
          </Button>
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
  onPublish,
}: {
  exams: Exam[];
  selected: Exam | null;
  onNew: () => void;
  onEdit: (exam: Exam) => void;
  onDelete: (exam: Exam) => void;
  onStudents: (exam: Exam) => void;
  onPublish: (exam: Exam) => void;
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
                    <p className="font-mono text-xs font-semibold tracking-widest text-indigo-600">
                      Code: {exam.exam_code}
                    </p>
                    <p className="text-xs text-slate-500">
                      grips://exam.cs.ac.cn
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
                      {(exam.state === "draft" || exam.state === "scheduled") && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="text-emerald-700 hover:bg-emerald-50"
                          onClick={() => onPublish(exam)}
                        >
                          发布
                        </Button>
                      )}
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
  examAdmins,
  users,
  onSelect,
  onAdd,
  onRefresh,
}: {
  exams: Exam[];
  selected: Exam | null;
  students: Student[];
  examAdmins: ExamAdmin[];
  users: components["schemas"]["User"][];
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
                  ? "只有启用的全局用户可以通过 OIDC 认证后进入考试；尚未登录的邮箱用户也可以提前加入。"
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
          <CardContent className="space-y-6 p-0">
            <div className="border-b border-slate-100 px-6 pt-5">
              <div className="mb-3 flex items-center justify-between">
                <div>
                  <h3 className="font-medium text-slate-900">考试管理员</h3>
                  <p className="text-xs text-slate-500">该能力只作用于当前考试，可与平台管理员和参加者身份叠加。</p>
                </div>
              </div>
              <div className="mb-4 flex flex-wrap gap-2">
                {examAdmins.map((admin) => (
                  <Badge key={admin.user.id} variant="warning" className="gap-2 py-1">
                    {admin.user.email || admin.user.display_name || admin.user.id}
                    <button type="button" className="text-amber-900/70 hover:text-amber-950" onClick={() => void (async () => {
                      const result = await api.DELETE("/admin/api/exams/{examId}/admins/{userId}", { params: { path: { examId: selected.id, userId: admin.user.id } } });
                      if (result.error) toast.add({ title: "撤销考试管理员失败", type: "error" }); else onRefresh();
                    })()} aria-label="撤销考试管理员">×</button>
                  </Badge>
                ))}
                {!examAdmins.length && <span className="text-xs text-slate-400">尚未配置考试管理员</span>}
              </div>
              <div className="mb-5 flex max-w-xl gap-2">
                <SelectField
                  value=""
                  placeholder="选择全局用户并授予考试管理员"
                  options={users.filter((user) => !examAdmins.some((admin) => admin.user.id === user.id)).map((user) => ({ value: user.id, label: user.email || user.display_name || user.id }))}
                  onValueChange={(userId) => void (async () => {
                    if (!userId) return;
                    const result = await api.PUT("/admin/api/exams/{examId}/admins/{userId}", { params: { path: { examId: selected.id, userId } }, body: { enabled: true } });
                    if (result.error) toast.add({ title: "授予考试管理员失败", type: "error" }); else onRefresh();
                  })()}
                />
              </div>
            </div>
            <div className="flex items-center justify-between border-b border-slate-100 bg-slate-50/60 px-6 py-3 text-xs text-slate-500">
              <span>
                {students.length
                  ? `已配置 ${students.length} 名学生`
                  : "尚未配置白名单（默认拒绝参加考试）"}
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

function UsersPage({users, onRefresh}: {users: components["schemas"]["User"][]; onRefresh: () => void}) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [platformAdmin, setPlatformAdmin] = useState(false);
  const [saving, setSaving] = useState(false);
  const invite = async (event: FormEvent) => {
    event.preventDefault(); setSaving(true);
    const result = await api.POST("/admin/api/users", {body: {email: email.trim(), display_name: name.trim(), platform_admin: platformAdmin}});
    setSaving(false);
    if (result.error) { toast.add({title: "添加用户失败", description: "邮箱可能已经存在，或格式不正确。", type: "error"}); return; }
    setEmail(""); setName(""); setPlatformAdmin(false); toast.add({title: "用户已加入全局用户库", type: "success"}); onRefresh();
  };
  const update = async (user: components["schemas"]["User"], patch: {enabled?: boolean; platform_admin?: boolean}) => {
    const result = await api.PATCH("/admin/api/users/{userId}", {params: {path: {userId: user.id}}, body: patch});
    if (result.error) toast.add({title: "更新用户失败", type: "error"}); else onRefresh();
  };
  return <>
    <PageHeading title="全局用户管理" description="按邮箱预先建档；用户首次通过 Connect OIDC 登录后自动绑定 subject。" />
    <Card className="mb-6"><CardHeader><CardTitle>预先添加用户</CardTitle><CardDescription>邮箱必须来自 OIDC 返回的已验证 email claim。考试管理员在具体考试中单独配置，和平台管理员、普通用户身份可以叠加。</CardDescription></CardHeader><CardContent><form className="grid gap-3 sm:grid-cols-[1fr_1fr_auto_auto]" onSubmit={(e) => void invite(e)}>
      <Input type="email" required placeholder="student@example.edu.cn" value={email} onChange={e=>setEmail(e.target.value)} />
      <Input placeholder="显示名称（可选）" value={name} onChange={e=>setName(e.target.value)} />
      <label className="flex items-center gap-2 rounded-md border px-3 text-sm"><input type="checkbox" checked={platformAdmin} onChange={e=>setPlatformAdmin(e.target.checked)} />平台管理员</label>
      <Button type="submit" disabled={saving}>{saving ? "添加中…" : "添加用户"}</Button>
    </form></CardContent></Card>
    <Card><CardHeader><CardTitle>用户目录</CardTitle><CardDescription>平台管理员是全局能力；考试管理员在每场考试单独配置；普通用户可以同时具备考试管理员或平台管理员能力。</CardDescription></CardHeader><CardContent className="p-0"><Table><TableHeader><TableRow><TableHead>用户</TableHead><TableHead>邮箱</TableHead><TableHead>OIDC Subject</TableHead><TableHead>平台能力</TableHead><TableHead>状态</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader><TableBody>{users.map(user=><TableRow key={user.id}><TableCell><div className="font-medium">{user.display_name || "未命名"}</div><div className="font-mono text-[11px] text-slate-400">{user.id}</div></TableCell><TableCell>{user.email || "—"}</TableCell><TableCell className="max-w-xs truncate font-mono text-xs text-slate-500">{user.subject || "尚未登录绑定"}</TableCell><TableCell><Badge variant={user.platform_admin ? "warning" : "secondary"}>{user.platform_admin ? "平台管理员" : "普通用户"}</Badge></TableCell><TableCell><Badge variant={user.enabled ? "success" : "destructive"}>{user.enabled ? "启用" : "停用"}</Badge></TableCell><TableCell><div className="flex justify-end gap-1"><Button size="sm" variant="ghost" onClick={()=>void update(user,{enabled:!user.enabled})}>{user.enabled ? "停用" : "启用"}</Button>{user.subject && <Button size="sm" variant="ghost" onClick={()=>void update(user,{platform_admin:!user.platform_admin})}>{user.platform_admin ? "取消平台管理员" : "设为平台管理员"}</Button>}</div></TableCell></TableRow>)}{!users.length&&<TableRow><TableCell colSpan={6} className="py-14 text-center text-slate-500">暂无用户</TableCell></TableRow>}</TableBody></Table></CardContent></Card>
  </>;
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
    const result = await api.PUT("/admin/api/exams/{examId}/participants/{userId}", {
      params: { path: { examId, userId: student.subject } },
      body: { enabled: !student.enabled },
    });
    if (result.error) {
      toast.add({ title: "更新学生失败", description: student.subject, type: "error" });
      return;
    }
    toast.add({ title: student.enabled ? "学生已禁用" : "学生已启用", type: "success" });
    onChanged();
  };
  const remove = async () => {
    const result = await api.DELETE("/admin/api/exams/{examId}/participants/{userId}", {
      params: { path: { examId, userId: student.subject } },
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
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              void remove();
              setConfirmOpen(false);
            }}
          >
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
  const [examCode, setExamCode] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [state, setState] = useState<Exam["state"]>("draft");
  const [starts, setStarts] = useState("");
  const [ends, setEnds] = useState("");
  const [policy, setPolicy] = useState("{}");
  const [requireFullscreen, setRequireFullscreen] = useState(false);
  const [lockFullscreen, setLockFullscreen] = useState(false);
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!open) return;
    setId(exam?.id || "");
    setExamCode(exam?.exam_code || "");
    setBaseURL(exam?.base_url || "");
    setState(exam?.state || "draft");
    setStarts(exam?.starts_at ? exam.starts_at.slice(0, 16) : "");
    setEnds(exam?.ends_at ? exam.ends_at.slice(0, 16) : "");
    const examPolicy = exam?.policy as
      | { browser?: { require_fullscreen?: unknown; lock_fullscreen?: unknown } }
      | undefined;
    setPolicy(exam?.policy ? JSON.stringify(exam.policy, null, 2) : "{}");
    setRequireFullscreen(examPolicy?.browser?.require_fullscreen === true);
    setLockFullscreen(examPolicy?.browser?.lock_fullscreen === true);
    setFormError("");
  }, [exam, open]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (saving) return;
    setFormError("");
    let policyValue: Record<string, unknown>;
    try {
      policyValue = JSON.parse(policy);
    } catch {
      setFormError("策略必须是合法 JSON。");
      return;
    }
    const browserPolicy = policyValue.browser;
    if (
      browserPolicy !== undefined &&
      (typeof browserPolicy !== "object" ||
        browserPolicy === null ||
        Array.isArray(browserPolicy))
    ) {
      setFormError("策略中的 browser 必须是 JSON 对象。");
      return;
    }
    policyValue.browser = {
      ...(browserPolicy as Record<string, unknown> | undefined),
      require_fullscreen: requireFullscreen,
      lock_fullscreen: requireFullscreen && lockFullscreen,
    };
    if (!id.trim() || !baseURL.trim()) {
      setFormError("考试 ID 和源站 URL 不能为空。");
      return;
    }
    if (!/^[A-Za-z0-9._-]{1,128}$/.test(id.trim())) {
      setFormError("考试 ID 只能包含字母、数字、点、下划线和连字符。");
      return;
    }
    try {
      const parsed = new URL(baseURL.trim());
      if (parsed.protocol !== "https:" || (parsed.pathname !== "" && parsed.pathname !== "/") || parsed.search || parsed.hash || parsed.username || parsed.password) {
        throw new Error("unsupported protocol");
      }
    } catch {
      setFormError("透明 TLS 源站必须是 HTTPS origin（不能带路径、查询参数或凭据），例如 https://cs101.gbu.edu.cn。");
      return;
    }
    setSaving(true);
    const body = {
      id: id.trim(),
      exam_code: examCode.trim().toUpperCase() || undefined,
      base_url: baseURL.trim(),
      state,
      starts_at: starts ? new Date(starts).toISOString() : null,
      ends_at: ends ? new Date(ends).toISOString() : null,
      policy: policyValue,
    };
    try {
      const result = exam
        ? await api.PATCH("/admin/api/exams/{examId}", {
            params: { path: { examId: exam.id } },
            body,
          })
        : await api.POST("/admin/api/exams", { body });
      if (result.error) {
        setFormError("保存失败，请检查考试 ID、源站 URL 和管理员权限。");
        toast.add({ title: "保存考试失败", type: "error" });
        return;
      }
      toast.add({
        title: exam ? "考试已更新" : "考试已创建",
        description: id.trim(),
        type: "success",
      });
      onSaved();
    } catch {
      setFormError("无法连接 BYOD Server，请检查网络和服务状态。");
      toast.add({ title: "保存考试失败", description: "网络请求未完成。", type: "error" });
    } finally {
      setSaving(false);
    }
  };
  return (
    <AppDialog
      open={open}
      onClose={onClose}
      title={exam ? "编辑考试" : "新建考试"}
      description="配置考试源站、开放时间和浏览器策略"
    >
      <form
        className="space-y-4"
        noValidate
        onSubmit={(event) => void submit(event)}
      >
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
          <Label htmlFor="exam-code">考试识别码（8 位 Base36）</Label>
          <Input
            id="exam-code"
            value={examCode}
            maxLength={8}
            onChange={(event) => setExamCode(event.target.value.toUpperCase())}
            placeholder="保存时自动生成"
            className="font-mono tracking-widest"
          />
          <p className="text-xs text-slate-500">学生在 grips://exam.cs.ac.cn 中输入此识别码。</p>
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
        <div className="flex items-center justify-between gap-4 rounded-lg border border-slate-200 bg-slate-50/70 px-4 py-3">
          <div className="space-y-1">
            <Label htmlFor="exam-require-fullscreen">进入考试后自动全屏</Label>
            <p className="text-xs text-slate-500">
              身份认证和策略加载成功后，将整个 BYOD Browser 窗口切换为全屏；考试结束时恢复原状态。
            </p>
          </div>
          <Switch
            id="exam-require-fullscreen"
            checked={requireFullscreen}
            onCheckedChange={(checked) => {
              setRequireFullscreen(checked);
              if (!checked) setLockFullscreen(false);
            }}
          />
        </div>
        <div className="flex items-center justify-between gap-4 rounded-lg border border-amber-200 bg-amber-50/70 px-4 py-3">
          <div className="space-y-1">
            <Label htmlFor="exam-lock-fullscreen">考试期间禁止退出全屏</Label>
            <p className="text-xs text-amber-800/80">
              启用后 Esc、F11 和浏览器菜单的退出全屏操作会被拦截；必须先结束考试或由监考端解除策略。
            </p>
          </div>
          <Switch
            id="exam-lock-fullscreen"
            checked={lockFullscreen}
            disabled={!requireFullscreen}
            onCheckedChange={setLockFullscreen}
          />
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
            策略会在签名后下发给 BYOD Browser；上面的开关会写入
            browser.require_fullscreen 和 browser.lock_fullscreen，其余高级配置可在这里编辑。
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
  users,
  onClose,
  onSaved,
}: {
  open: boolean;
  exam: Exam | null;
  users: components["schemas"]["User"][];
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
      "/admin/api/exams/{examId}/participants/{userId}",
      {
        params: { path: { examId: exam.id, userId: subject.trim() } },
        body: { enabled: true },
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
          <Label htmlFor="student-subject">全局用户 ID</Label>
          <SelectField
            value={subject}
            onValueChange={setSubject}
            placeholder="选择全局用户（按邮箱）"
            options={users.map((user) => ({
              value: user.id,
              label: `${user.email || user.display_name || "未命名"}${user.subject ? "" : "（尚未登录）"}`,
            }))}
          />
          <p className="text-xs text-slate-500">
            先在“用户管理”按邮箱添加用户，再把用户 ID 加入考试名单。
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
