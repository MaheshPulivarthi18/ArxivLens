import React, { useState, useEffect } from 'react';
import { AlertCircle } from 'lucide-react';
import Navbar from './components/Navbar';
import HeroSearch from './components/HeroSearch';
import SynthesisView from './components/SynthesisView';
import ReferencesStack from './components/ReferencesStack';
import PapersLibrary from './components/PapersLibrary';
import InfrastructureModal from './components/InfrastructureModal';
import IngestModal from './components/IngestModal';
import Toast from './components/Toast';

export default function App() {
  const [activeTab, setActiveTab] = useState('search');
  const [adminOpen, setAdminOpen] = useState(false);
  const [ingestOpen, setIngestOpen] = useState(false);
  const [searchResult, setSearchResult] = useState(null);
  const [loading, setLoading] = useState(false);
  const [earlyRefusal, setEarlyRefusal] = useState(false);
  const [lastQuestion, setLastQuestion] = useState('How does G-CARL improve patient-oriented medical report interpretation?');
  const [discovering, setDiscovering] = useState(false);
  const [activeMarker, setActiveMarker] = useState(1);
  const [toastMessage, setToastMessage] = useState('');

  const showToast = (msg) => {
    setToastMessage(msg);
    setTimeout(() => {
      setToastMessage('');
    }, 2800);
  };

  const handleCopy = (text, msg) => {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(() => {
        showToast(msg);
      }).catch(() => {
        fallbackCopy(text, msg);
      });
    } else {
      fallbackCopy(text, msg);
    }
  };

  const fallbackCopy = (text, msg) => {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.left = "-99999px";
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    try {
      document.execCommand('copy');
      showToast(msg);
    } catch (e) {
      console.error(e);
    }
    document.body.removeChild(ta);
  };

  const handleSearch = async (question) => {
    setLastQuestion(question);
    setLoading(true);
    setEarlyRefusal(false);
    setSearchResult(null);

    try {
      const resp = await fetch('/v1/ask', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ question, max_citations: 5 })
      });
      const data = await resp.json();

      if (data.answer && data.answer.includes("No indexed papers are relevant")) {
        setEarlyRefusal(true);
      } else {
        setSearchResult(data);
        if (data.citations && data.citations.length > 0) {
          setActiveMarker(data.citations[0].marker);
        }
      }
    } catch (err) {
      console.error("Search request failed:", err);
      showToast("❌ Search request failed. Make sure backend is running.");
    } finally {
      setLoading(false);
    }
  };

  const handleDiscoverAndSearch = async () => {
    if (!lastQuestion) return;
    setDiscovering(true);
    showToast(`🔍 Searching arXiv for papers on "${lastQuestion}"...`);
    try {
      const resp = await fetch('/v1/papers/discover', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: lastQuestion })
      });
      const data = await resp.json();
      if (!resp.ok || data.error) {
        throw new Error(data.error || 'No matching papers found on arXiv');
      }
      showToast(`✅ Indexed "${data.title.substring(0, 30)}..."! Synthesizing...`);
      // Now re-run search on newly indexed paper!
      handleSearch(lastQuestion);
    } catch (err) {
      console.error(err);
      showToast(`❌ ${err.message}`);
    } finally {
      setDiscovering(false);
    }
  };

  // Run initial default query on mount
  useEffect(() => {
    handleSearch("How does G-CARL improve patient-oriented medical report interpretation?");
  }, []);

  const handleCitationClick = (marker) => {
    setActiveMarker(marker);
    const targetElement = document.getElementById(`ref-${marker}`);
    if (targetElement) {
      targetElement.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
  };

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col font-sans selection:bg-emerald-500/20 selection:text-emerald-300">
      <Navbar
        activeTab={activeTab}
        setActiveTab={setActiveTab}
        onOpenAdmin={() => setAdminOpen(true)}
        onOpenIngest={() => setIngestOpen(true)}
      />

      <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-6">
        {activeTab === 'search' && (
          <div className="space-y-8">
            <HeroSearch onSearch={handleSearch} loading={loading} />

            {earlyRefusal && (
              <div className="max-w-2xl mx-auto p-5 rounded-2xl bg-amber-500/10 border border-amber-500/30 text-amber-200 text-sm space-y-3 animate-in fade-in duration-200">
                <div className="flex items-start gap-3">
                  <AlertCircle className="w-5 h-5 text-amber-400 shrink-0 mt-0.5" />
                  <div>
                    <strong className="font-semibold text-amber-300">Relevance Protection Activated:</strong>
                    <p className="text-xs text-amber-200/90 mt-0.5">
                      No locally indexed papers meet the strict semantic relevance threshold for <code className="bg-amber-950/60 px-1.5 py-0.5 rounded text-amber-300">"{lastQuestion}"</code>. Incidental appendix mentions have been cleanly filtered out.
                    </p>
                  </div>
                </div>

                <div className="pt-2 border-t border-amber-500/20 flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                  <span className="text-xs text-muted-foreground">Would you like to fetch authoritative papers from arXiv?</span>
                  <button
                    type="button"
                    disabled={discovering}
                    onClick={handleDiscoverAndSearch}
                    className="inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 text-white shadow-md shadow-emerald-950/50 transition-all disabled:opacity-50 cursor-pointer shrink-0"
                  >
                    <span>⚡ Search arXiv for "{lastQuestion}" & Index</span>
                  </button>
                </div>
              </div>
            )}

            {searchResult && (
              <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 items-start">
                <div className="lg:col-span-7">
                  <SynthesisView
                    answer={searchResult.answer}
                    traceId={searchResult.trace_id}
                    citations={searchResult.citations}
                    onCitationClick={handleCitationClick}
                    onCopy={handleCopy}
                  />
                </div>

                <div className="lg:col-span-5">
                  <ReferencesStack
                    citations={searchResult.citations}
                    activeMarker={activeMarker}
                    onCopy={handleCopy}
                  />
                </div>
              </div>
            )}
          </div>
        )}

        {activeTab === 'library' && (
          <PapersLibrary onBackToSearch={() => setActiveTab('search')} />
        )}
      </main>

      <footer className="border-t border-border/60 py-6 text-center text-xs text-muted-foreground">
        <p>ArxivLens Clinical AI Studio • Powered by PostgreSQL pgvector HNSW, Kafka 3.7 & PyTorch MPS</p>
      </footer>

      <InfrastructureModal
        isOpen={adminOpen}
        onClose={() => setAdminOpen(false)}
        onShowToast={showToast}
      />

      <IngestModal
        isOpen={ingestOpen}
        onClose={() => setIngestOpen(false)}
        onIngestSuccess={(res) => {
          showToast(`✅ Successfully indexed "${res.title.substring(0, 30)}..."!`);
        }}
        onAskAboutPaper={(question) => {
          setActiveTab('search');
          handleSearch(question);
        }}
      />

      <Toast message={toastMessage} />
    </div>
  );
}
