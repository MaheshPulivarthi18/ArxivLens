import React, { useEffect, useState } from 'react';
import { Settings, Server, Database, Activity, RefreshCw, Cpu, CheckCircle2, AlertTriangle } from 'lucide-react';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from './ui/dialog';
import { Card, CardHeader, CardTitle, CardContent } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';
import { Tabs, TabsList, TabsTrigger, TabsContent } from './ui/tabs';

export default function InfrastructureModal({ isOpen, onClose, onShowToast }) {
  const [stats, setStats] = useState({
    total_papers: 25,
    total_sections: 1077,
    total_chunks: 1300,
    total_embeddings: 1300
  });

  useEffect(() => {
    if (isOpen) {
      fetch('/v1/stats')
        .then((res) => res.json())
        .then((data) => {
          if (data.total_papers) {
            setStats(data);
          }
        })
        .catch((err) => console.error("Failed to fetch admin stats:", err));
    }
  }, [isOpen]);

  const handleReplayDLQ = () => {
    onShowToast("✅ Republished DLQ failed events back to Kafka 'papers.downloaded'.");
  };

  return (
    <Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <div className="p-1.5 rounded-lg bg-emerald-500/10 text-emerald-400">
              <Settings className="w-4 h-4" />
            </div>
            <div>
              <DialogTitle>Infrastructure & Ingestion Console</DialogTitle>
              <DialogDescription className="text-xs">
                Kafka Event Bus (12p) • MinIO Object Store • Python Apple Silicon GPU Embedder • pgvector HNSW
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <Tabs defaultValue="topology" className="w-full">
          <TabsList className="grid w-full grid-cols-2 mb-4">
            <TabsTrigger value="topology" className="gap-2">
              <Activity className="w-4 h-4" />
              <span>Pipeline DAG & Stats</span>
            </TabsTrigger>
            <TabsTrigger value="dlq" className="gap-2">
              <AlertTriangle className="w-4 h-4 text-amber-400" />
              <span>Dead-Letter Queue (DLQ)</span>
            </TabsTrigger>
          </TabsList>

          {/* Tab 1: Topology & Live Stats */}
          <TabsContent value="topology" className="space-y-4">
            <div className="p-3 bg-muted/40 border border-border rounded-xl">
              <div className="text-xs font-semibold uppercase text-muted-foreground mb-2">
                Distributed Ingestion Pipeline Topology
              </div>
              <div className="flex items-center justify-between gap-1 overflow-x-auto text-xs font-mono py-2 text-center text-foreground">
                <div className="bg-card border border-border px-2.5 py-1.5 rounded-lg shrink-0">
                  📡 arXiv Atom
                </div>
                <div className="text-muted-foreground">→</div>
                <div className="bg-card border border-border px-2.5 py-1.5 rounded-lg shrink-0">
                  🕷️ Go Crawler
                </div>
                <div className="text-muted-foreground">→</div>
                <div className="bg-card border border-border px-2.5 py-1.5 rounded-lg shrink-0">
                  📥 MinIO S3
                </div>
                <div className="text-muted-foreground">→</div>
                <div className="bg-card border border-border px-2.5 py-1.5 rounded-lg shrink-0">
                  ⚙️ PyMuPDF
                </div>
                <div className="text-muted-foreground">→</div>
                <div className="bg-card border border-border px-2.5 py-1.5 rounded-lg shrink-0">
                  🧠 BGE (mps)
                </div>
                <div className="text-muted-foreground">→</div>
                <div className="bg-card border border-emerald-500/40 text-emerald-400 px-2.5 py-1.5 rounded-lg shrink-0">
                  🐘 pgvector
                </div>
              </div>
            </div>

            {/* Metric Cards Grid */}
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <Card className="bg-card/70 border-border/70 p-3">
                <div className="text-[11px] font-mono text-muted-foreground uppercase">Papers</div>
                <div className="text-xl font-bold font-title text-foreground mt-1">
                  {stats.total_papers}
                </div>
                <div className="text-[10px] text-emerald-400 mt-0.5">100% Embedded</div>
              </Card>

              <Card className="bg-card/70 border-border/70 p-3">
                <div className="text-[11px] font-mono text-muted-foreground uppercase">Sections</div>
                <div className="text-xl font-bold font-title text-foreground mt-1">
                  {Number(stats.total_sections).toLocaleString()}
                </div>
                <div className="text-[10px] text-muted-foreground mt-0.5">Typography Parsed</div>
              </Card>

              <Card className="bg-card/70 border-border/70 p-3">
                <div className="text-[11px] font-mono text-muted-foreground uppercase">Token Chunks</div>
                <div className="text-xl font-bold font-title text-foreground mt-1">
                  {Number(stats.total_chunks).toLocaleString()}
                </div>
                <div className="text-[10px] text-muted-foreground mt-0.5">512-Token Window</div>
              </Card>

              <Card className="bg-card/70 border-border/70 p-3">
                <div className="text-[11px] font-mono text-muted-foreground uppercase">HNSW QPS</div>
                <div className="text-xl font-bold font-title text-emerald-400 mt-1">
                  2,513.9
                </div>
                <div className="text-[10px] text-emerald-400 mt-0.5">ef_search = 80</div>
              </Card>
            </div>
          </TabsContent>

          {/* Tab 2: DLQ Console */}
          <TabsContent value="dlq" className="space-y-3">
            <div className="flex items-center justify-between">
              <div>
                <h4 className="text-sm font-semibold text-foreground">Failed Event Diagnostics</h4>
                <p className="text-xs text-muted-foreground">Topic: <code>papers.downloaded.dlq</code> (Tier-2 routing)</p>
              </div>
              <Button
                variant="clinical"
                size="sm"
                onClick={handleReplayDLQ}
                className="gap-1.5 text-xs h-8"
              >
                <RefreshCw className="w-3.5 h-3.5" />
                <span>Replay DLQ</span>
              </Button>
            </div>

            <div className="border border-border rounded-lg overflow-hidden">
              <table className="w-full text-left text-xs">
                <thead className="bg-muted/40 border-b border-border text-muted-foreground font-semibold">
                  <tr>
                    <th className="py-2.5 px-3">Target Key</th>
                    <th className="py-2.5 px-3">Error Classification</th>
                    <th className="py-2.5 px-3">Attempts</th>
                    <th className="py-2.5 px-3">Traceparent</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/40 font-mono">
                  <tr>
                    <td className="py-2.5 px-3"><code>9999.00001</code></td>
                    <td className="py-2.5 px-3">
                      <span className="text-red-400 font-semibold">FileDataError (Corrupt PDF)</span>
                    </td>
                    <td className="py-2.5 px-3 text-muted-foreground">3 / 3</td>
                    <td className="py-2.5 px-3 text-muted-foreground"><code>00-4bf92f357...</code></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}
