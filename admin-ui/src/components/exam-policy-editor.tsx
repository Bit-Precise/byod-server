import { Plus, RotateCcw, X } from "lucide-react";
import { useEffect, useState } from "react";
import { Alert, AlertDescription } from "./ui/alert";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import {
  actionOptions, advancedFields, fullscreenFields, isPolicyObject,
  networkFields, policyValue, reservedBrowserFields, sessionFields,
  setFullscreenValue, setPolicyValue, violationFields,
  type ExamPolicy, type PolicyField,
} from "../lib/exam-policy";

type Props = {
  value: ExamPolicy;
  onChange: (value: ExamPolicy) => void;
  errors: Record<string, string>;
  disabled?: boolean;
};

function Choice({ id, value, options, onChange, invalid, descriptionId, disabled }: {
  id: string; value: string; options: { value: string; label: string }[];
  onChange: (value: string) => void; invalid: boolean; descriptionId: string; disabled?: boolean;
}) {
  return <Select value={value} items={options} onValueChange={(next) => { if (next !== null) onChange(next); }} disabled={disabled}>
    <SelectTrigger id={id} className="w-full sm:w-44" aria-invalid={invalid || undefined} aria-describedby={descriptionId}>
      <SelectValue />
    </SelectTrigger>
    <SelectContent>{options.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}</SelectContent>
  </Select>;
}

function PolicyInput({ field, value, onChange, error, disabled }: {
  field: PolicyField; value: unknown; onChange: (value: unknown) => void; error?: string; disabled?: boolean;
}) {
  const id = `policy-${field.path.replaceAll(".", "-")}`;
  const inherited = value === undefined;
  const reset = <Button type="button" variant="ghost" size="sm" disabled={disabled || inherited} onClick={() => onChange(undefined)} aria-label={`${field.label}：继承服务端配置`}>
    <RotateCcw className="size-3.5" />继承配置
  </Button>;
  const isList = ["hosts", "origins", "paths", "schemes"].includes(field.kind);
  let control;
  if (field.kind === "boolean" || field.kind === "action") {
    const choices = field.kind === "boolean" ? [{ value: "true", label: "开启" }, { value: "false", label: "关闭" }] : actionOptions;
    const validValue = field.kind === "boolean" ? typeof value === "boolean" : typeof value === "string";
    const options = [{ value: "inherit", label: "继承服务端配置" }, ...choices.map((option) => ({ ...option, value: `explicit:${option.value}` }))];
    if (!inherited && !validValue) options.push({ value: "invalid", label: "原配置无效，请重选" });
    if (field.kind === "action" && typeof value === "string" && !choices.some((option) => option.value === value)) options.push({ value: `explicit:${value}`, label: `保留旧值：${value}` });
    control = <Choice id={id} value={inherited ? "inherit" : validValue ? `explicit:${String(value)}` : "invalid"} options={options}
      onChange={(next) => { if (next !== "invalid") onChange(next === "inherit" ? undefined : field.kind === "boolean" ? next === "explicit:true" : next.slice(9)); }}
      disabled={disabled} invalid={!!error} descriptionId={`${id}-help`} />;
  } else if (field.kind === "integer") {
    control = <div className="flex flex-wrap items-center gap-1">
      <Input id={id} type="number" className="w-32" min={field.min} max={field.max} step={1}
        value={typeof value === "number" || typeof value === "string" ? value : ""} disabled={disabled}
        placeholder={`继承（内置 ${field.fallback}）`} aria-invalid={!!error || undefined} aria-describedby={`${id}-help`}
        onChange={(event) => onChange(event.target.value === "" ? undefined : Number(event.target.value))} />{reset}
    </div>;
  } else {
    const validList = inherited || (field.kind === "hosts" && value === null) || (Array.isArray(value) && value.every((item) => typeof item === "string"));
    const entries: string[] = Array.isArray(value) && validList ? value : [];
    const placeholder = { hosts: "cs101.gbu.edu.cn", origins: "https://iaaa.gbu.edu.cn", paths: "/course-101/**", schemes: "file" }[field.kind as "hosts" | "origins" | "paths" | "schemes"];
    control = <div className="mt-3 space-y-2" role="group" aria-labelledby={`${id}-label`}>
      {validList && entries.map((entry, index) => <div className="flex items-center gap-2" key={index}>
        <Input id={`${id}-${index}`} value={entry} placeholder={placeholder} spellCheck={false} disabled={disabled}
          aria-label={`${field.label} ${index + 1}`} aria-invalid={!!error || undefined} aria-describedby={`${id}-help`}
          onChange={(event) => onChange(entries.map((item, i) => i === index ? event.target.value : item))}
          onPaste={(event) => {
            const pasted = event.clipboardData.getData("text");
            if (!/[\r\n,]/.test(pasted)) return;
            event.preventDefault();
            const next = pasted.split(/[\r\n,]+/).map((item) => item.trim()).filter(Boolean);
            onChange([...entries.slice(0, index), ...next, ...entries.slice(index + 1)]);
          }} />
        <Button type="button" variant="ghost" size="icon" disabled={disabled} onClick={() => onChange(entries.filter((_, i) => i !== index))} aria-label={`删除${field.label} ${index + 1}`}><X className="size-4" /></Button>
      </div>)}
      {validList && entries.length === 0 && <p className="rounded-md bg-slate-50 px-3 py-2 text-xs text-slate-500">
        {inherited ? "当前继承服务端配置；添加项目后使用自定义列表。" : field.kind === "hosts" ? "自定义列表为空：不启用 tunnel。" : "自定义列表为空。"}
      </p>}
      <div className="flex flex-wrap items-center gap-1">
        {validList && <Button type="button" size="sm" variant="outline" disabled={disabled} onClick={() => onChange([...entries, ""])}><Plus className="size-3.5" />添加{field.kind === "origins" ? "网站" : field.kind === "hosts" ? "主机" : field.kind === "schemes" ? "协议" : "路径"}</Button>}
        {reset}
        <Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => onChange([])} aria-label={`${field.label}：使用空列表`}>{validList ? "使用空列表" : "重置为空列表"}</Button>
      </div>
    </div>;
  }
  return <div className="space-y-2 rounded-lg border border-slate-200 p-3" data-policy-field={field.path}>
    <div className={isList ? "space-y-1" : "flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-4"}>
      <div className="space-y-1">
        {isList ? <p id={`${id}-label`} className="text-sm font-medium">{field.label}</p> : <Label htmlFor={id}>{field.label}</Label>}
        <p id={`${id}-help`} className="text-xs leading-relaxed text-slate-500">
          {field.description}
          {field.reserved && <span className="block text-amber-700">预留项：仅保存并下发，当前版本未按该项执行。</span>}
          {field.kind === "boolean" && <span className="block">内置默认：{field.fallback ? "开启" : "关闭"}；选择继承时以服务端实际配置为准。</span>}
        </p>
      </div>
      {!isList && control}
    </div>
    {isList && control}
    {error && <p id={`${id}-error`} role="alert" className="text-xs text-red-700">{error}</p>}
  </div>;
}

export function ExamPolicyEditor({ value, onChange, errors, disabled }: Props) {
  const [sessionOpen, setSessionOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  useEffect(() => {
    if (sessionFields.some((field) => !!errors[field.path])) setSessionOpen(true);
    if ([...reservedBrowserFields, ...advancedFields, ...violationFields].some((field) => !!errors[field.path])) setAdvancedOpen(true);
  }, [errors]);
  const renderField = (field: PolicyField) => <PolicyInput key={field.path} field={field} value={policyValue(value, field.path)} disabled={disabled} error={errors[field.path]}
    onChange={(next) => onChange(setFullscreenValue(value, field.path, next))} />;
  return <section className="space-y-4" aria-labelledby="exam-policy-heading">
    <div className="space-y-1 border-t border-slate-200 pt-4">
      <h3 id="exam-policy-heading" className="font-semibold">浏览器策略</h3>
      <p className="text-xs leading-relaxed text-slate-500">无需编写 JSON。未自定义的项目继承服务端配置；已有扩展字段会原样保留，不会因编辑本表单丢失。</p>
    </div>
    {["browser", "navigation", "session", "violations"].filter((section) => value[section] !== undefined && !isPolicyObject(value[section])).map((section) => <Alert key={section} variant="destructive">
      <AlertDescription>{section}：{errors[section] || "旧配置格式不正确，请先恢复此组默认配置。"}
        <Button type="button" size="sm" variant="outline" disabled={disabled} onClick={() => onChange(setPolicyValue(value, section, undefined))}>恢复 {section} 默认配置</Button>
      </AlertDescription>
    </Alert>)}
    <fieldset className="space-y-2"><legend className="mb-2 text-sm font-semibold">全屏控制</legend>{fullscreenFields.map(renderField)}</fieldset>
    <fieldset className="space-y-2"><legend className="mb-2 text-sm font-semibold">网站访问与网络路由</legend>{networkFields.map(renderField)}</fieldset>
    <details className="rounded-lg border border-slate-200 p-3" open={sessionOpen} onToggle={(event) => setSessionOpen(event.currentTarget.open)}>
      <summary className="cursor-pointer text-sm font-semibold">会话与心跳</summary>
      <div className="mt-3 space-y-2">{sessionFields.map(renderField)}</div>
    </details>
    <details className="rounded-lg border border-slate-200 p-3" open={advancedOpen} onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}>
      <summary className="cursor-pointer text-sm font-semibold">其他策略与兼容设置</summary>
      <div className="mt-3 space-y-4">
        <Alert><AlertDescription>下面的浏览器权限、禁止协议和违规处理属于预留配置：会保存并下发，但当前版本尚未按这些字段执行。修改它们不会改变现有浏览器限制。当前服务端收到后台切换或开发者工具违规时仍会暂停考试。</AlertDescription></Alert>
        <fieldset className="space-y-2"><legend className="mb-2 flex items-center gap-2 text-sm font-semibold">浏览器权限<Badge variant="secondary">预留</Badge></legend>{reservedBrowserFields.map(renderField)}</fieldset>
        <fieldset className="space-y-2"><legend className="mb-2 text-sm font-semibold">导航兼容设置</legend>{advancedFields.filter((field) => field.path !== "allowed_paths" || policyValue(value, field.path) !== undefined).map(renderField)}</fieldset>
        <fieldset className="space-y-2"><legend className="mb-2 flex items-center gap-2 text-sm font-semibold">违规处理<Badge variant="secondary">预留</Badge></legend>{violationFields.map(renderField)}</fieldset>
      </div>
    </details>
  </section>;
}
