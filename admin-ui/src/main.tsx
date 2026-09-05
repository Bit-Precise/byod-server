import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { api } from "./api/client";
import { Button } from "./components/ui/button";
import "./index.css";

type Exam = { id: string; base_url: string; state: string; starts_at?: string | null; ends_at?: string | null };

function App() {
  const [exams, setExams] = useState<Exam[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();
  const load = async () => {
    setLoading(true);
    const { data, error: requestError } = await api.GET("/admin/api/exams");
    if (requestError) setError("无法加载考试列表，请检查管理员登录状态。");
    else setExams((data ?? []) as Exam[]);
    setLoading(false);
  };
  useEffect(() => { void load(); }, []);
  return <div className="min-h-screen"><header className="border-b bg-white"><div className="mx-auto flex max-w-7xl items-center justify-between px-8 py-5"><div><p className="text-xs font-semibold uppercase tracking-widest text-indigo-600">BYOD Server</p><h1 className="text-2xl font-semibold">考试管理后台</h1></div><Button onClick={() => void load()}>刷新</Button></div></header><main className="mx-auto max-w-7xl space-y-6 px-8 py-8"><div className="grid gap-4 md:grid-cols-3"><Metric label="考试总数" value={String(exams.length)} /><Metric label="进行中" value={String(exams.filter((exam) => exam.state === "active").length)} /><Metric label="在线 session" value="—" /></div><section className="rounded-xl border bg-white shadow-sm"><div className="flex items-center justify-between border-b px-6 py-4"><div><h2 className="font-semibold">考试</h2><p className="text-sm text-slate-500">管理考试源站、策略和参与学生</p></div><Button>新建考试</Button></div>{error && <p className="px-6 py-4 text-sm text-red-600">{error}</p>}{loading ? <p className="px-6 py-8 text-sm text-slate-500">加载中…</p> : <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead className="bg-slate-50 text-xs uppercase text-slate-500"><tr><th className="px-6 py-3">考试 ID</th><th className="px-6 py-3">源站</th><th className="px-6 py-3">状态</th><th className="px-6 py-3">操作</th></tr></thead><tbody className="divide-y">{exams.map((exam) => <tr key={exam.id}><td className="px-6 py-4 font-medium">{exam.id}</td><td className="px-6 py-4 text-slate-600">{exam.base_url}</td><td className="px-6 py-4"><span className="rounded-full bg-slate-100 px-2 py-1 text-xs">{exam.state}</span></td><td className="px-6 py-4"><Button variant="ghost">查看详情</Button></td></tr>)}{exams.length === 0 && <tr><td colSpan={4} className="px-6 py-10 text-center text-slate-500">暂无考试</td></tr>}</tbody></table></div>}</section></main></div>;
}

function Metric({ label, value }: { label: string; value: string }) { return <div className="rounded-xl border bg-white p-5 shadow-sm"><p className="text-sm text-slate-500">{label}</p><p className="mt-2 text-3xl font-semibold">{value}</p></div>; }
createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
