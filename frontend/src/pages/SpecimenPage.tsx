import { useEffect, useMemo, useState } from 'react';
import type { DomainRecord } from '../types/domain';
import { nextStatus, formatDate } from '../utils/format';
import { StatusBadge } from '../components/common/StatusBadge';
import { RiskTag } from '../components/common/RiskTag';
import { EmptyState } from '../components/common/EmptyState';
import { MetricCard } from '../components/common/MetricCard';
import { ConfirmDialog } from '../components/common/ConfirmDialog';
import { UiButton } from '../components/common/UiButton';
import { SplitDialog } from '../components/SplitDialog';
import { useAuth } from '../hooks/useAuth';
import { useSpecimenStore } from '../stores/specimen';
import { ENTITY_CONFIGS } from '../types/status';

const SPLITTABLE = new Set(['received', 'testing']);

export default function SpecimenPage() {
  const config = ENTITY_CONFIGS[1];
  const { items, meta, loading, error, load, createRecord, transition } = useSpecimenStore();
  const { session, hasRole } = useAuth();
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [pending, setPending] = useState<{ item: DomainRecord; status: string } | null>(null);
  const [splitting, setSplitting] = useState<DomainRecord | null>(null);

  useEffect(() => { void load(config.path); }, [config.path, load]);
  const canOperate = hasRole('operator');
  const highRisk = useMemo(() => items.filter((item) => ['high', 'critical'].includes(item.riskLevel)).length, [items]);
  const splitMothers = useMemo(() => items.filter((item) => (item.children?.length ?? 0) > 0).length, [items]);
  const codeById = useMemo(() => new Map(items.map((item) => [item.id, item.code])), [items]);

  const createDemo = async () => {
    await createRecord(config.path, {
      code: `SP-${Date.now().toString().slice(-6)}`,
      name: '新增检验样本',
      description: '通过前端工作台创建的检验样本记录',
      facility: '默认检验区', owner: session?.displayName || '现场操作员', category: '常规', riskLevel: 'medium',
      metricValue: 25, metricUnit: 'unit', effectiveAt: new Date().toISOString(), evidence: '已完成创建前证据核对', relatedCode: '',
      availableAmount: 50, availableUnit: 'mL',
    });
    setSearch('');
    setShowCreate(false);
  };

  const undisposedCount = (item: DomainRecord) => (item.children ?? []).filter((child) => child.status !== 'disposed').length;
  const splitBlocked = (item: DomainRecord) => {
    const open = undisposedCount(item);
    return open > 0 ? `尚有 ${open} 份未处置子样，母样不可处置` : '';
  };

  const confirmTransition = async () => {
    if (!pending) return;
    await transition(config.path, pending.item, pending.status);
    setPending(null);
  };

  return <main className="workspace">
    <header className="page-header">
      <div><p className="eyebrow">业务工作台</p><h1>{config.label}</h1><p>统一管理检验样本的接收、检测、分装、处置与风险证据。已接收或检测中的样本可按剩余可用量分装 2-5 份子样。</p></div>
      {canOperate ? <UiButton onClick={() => setShowCreate(true)}>新增{config.label}</UiButton> : <span className="access-note">只读权限</span>}
    </header>
    <section className="metrics">
      <MetricCard label="记录总数" value={meta.total} detail="母样与子样合计" />
      <MetricCard label="高风险" value={highRisk} detail="需要优先复核" />
      <MetricCard label="已分装母样" value={splitMothers} detail="含子样谱系" />
    </section>
    <section className="toolbar">
      <input aria-label="搜索" placeholder="搜索检验样本编码或名称" value={search} onChange={(event) => setSearch(event.target.value)} />
      <UiButton onClick={() => void load(config.path, search)}>查询</UiButton>
      <button className="link-button" onClick={() => { setSearch(''); void load(config.path); }}>重置</button>
    </section>
    {error && <div className="alert" role="alert">{error}</div>}
    <section className="table-shell" aria-busy={loading}>
      <table>
        <thead><tr><th>编码</th><th>名称</th><th>状态</th><th>风险</th><th>可用量</th><th>来源</th><th>子样清单</th><th>更新时间</th><th>操作</th></tr></thead>
        <tbody>
          {items.map((item) => {
            const next = nextStatus(item.status, config.primaryTransitions);
            const disposeBlocked = next === 'disposed' ? splitBlocked(item) : '';
            const blockedReason = item.blockedReason || disposeBlocked;
            const canSplit = canOperate && SPLITTABLE.has(item.status);
            return <tr key={item.id} className={item.parentId ? 'table-row--child' : ''}>
              <td><strong>{item.code}</strong>{(item.splitSeq ?? 0) > 0 && <small>子样 #{item.splitSeq}</small>}</td>
              <td>{item.name}<small>{item.facility}</small></td>
              <td><StatusBadge status={item.status} /></td>
              <td><RiskTag level={item.riskLevel} /></td>
              <td>{item.availableAmount ?? 0} {item.availableUnit || ''}</td>
              <td>{item.parentId ? <span title={`母样 ${codeById.get(item.parentId) || item.relatedCode}`}>{item.relatedCode || codeById.get(item.parentId) || '—'}</span> : <span className="muted">原始样本</span>}</td>
              <td>
                {(item.children?.length ?? 0) === 0 ? <span className="muted">—</span> : <ul className="child-list">
                  {item.children!.map((child) => <li key={child.id}>
                    <strong>{child.code}</strong>
                    <StatusBadge status={child.status} />
                    <small>{child.amount} {child.availableUnit}</small>
                  </li>)}
                </ul>}
                {blockedReason && <small className="blocked-reason" role="status">{blockedReason}</small>}
              </td>
              <td>{formatDate(item.updatedAt)}</td>
              <td>
                <div className="row-actions">
                  {next && canOperate && !disposeBlocked && <button className="table-action" onClick={() => setPending({ item, status: next })}>推进至 {next}</button>}
                  {canSplit && <button className="table-action" onClick={() => setSplitting(item)}>分装</button>}
                  {!canOperate && <span className="muted">只读</span>}
                  {disposeBlocked && <span className="muted" title={disposeBlocked}>不可处置</span>}
                </div>
              </td>
            </tr>;
          })}
          {!items.length && !loading && <EmptyState message="暂无记录" colSpan={9} />}
        </tbody>
      </table>
      {loading && <div className="loading">正在同步业务数据…</div>}
    </section>
    <ConfirmDialog open={showCreate} title={`新增${config.label}`} onCancel={() => setShowCreate(false)} onConfirm={() => void createDemo().catch(() => undefined)}>
      <p>将创建一条含 50 mL 初始可用量、完整责任人与证据信息的检验样本。</p>
    </ConfirmDialog>
    <ConfirmDialog open={Boolean(pending)} title="确认状态迁移" onCancel={() => setPending(null)} onConfirm={() => void confirmTransition().catch(() => undefined)}>
      <p>状态迁移会写入不可覆盖的版本与审计日志。</p>
      <strong>{pending?.item.status} → {pending?.status}</strong>
    </ConfirmDialog>
    <SplitDialog open={Boolean(splitting)} mother={splitting} onClose={() => setSplitting(null)} onDone={() => { setSplitting(null); void load(config.path, search); }} />
  </main>;
}
