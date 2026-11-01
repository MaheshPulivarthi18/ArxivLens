import React from 'react';
import { Microscope, BookOpen, Layers, Settings, PlusCircle, DownloadCloud } from 'lucide-react';
import { Button } from './ui/button';
import { Badge } from './ui/badge';

export default function Navbar({ activeTab, setActiveTab, onOpenAdmin, onOpenIngest }) {
  return (
    <header className="sticky top-0 z-40 w-full border-b border-border/80 bg-background/90 backdrop-blur-md">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-16 flex items-center justify-between">
        {/* Brand */}
        <div 
          className="flex items-center gap-3 cursor-pointer group"
          onClick={() => setActiveTab('search')}
        >
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-emerald-500 to-teal-700 flex items-center justify-center text-white shadow-lg shadow-emerald-900/20 group-hover:scale-105 transition-transform">
            <Microscope className="w-5 h-5" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <span className="font-title font-bold text-lg text-foreground tracking-tight">ArxivLens</span>
              <Badge variant="clinical" className="text-[10px] py-0 h-4">
                Clinical Research AI
              </Badge>
            </div>
            <p className="text-xs text-muted-foreground font-mono">Precision-Grounded Literature Engine</p>
          </div>
        </div>

        {/* Center & Right Navigation */}
        <div className="flex items-center gap-2 sm:gap-3">
          <Button
            variant={activeTab === 'search' ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => setActiveTab('search')}
            className={`gap-2 ${activeTab === 'search' ? 'bg-secondary font-semibold text-emerald-400' : 'text-muted-foreground'}`}
          >
            <BookOpen className="w-4 h-4" />
            <span className="hidden sm:inline">Literature Search</span>
          </Button>

          <Button
            variant={activeTab === 'library' ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => setActiveTab('library')}
            className={`gap-2 ${activeTab === 'library' ? 'bg-secondary font-semibold text-emerald-400' : 'text-muted-foreground'}`}
          >
            <Layers className="w-4 h-4" />
            <span className="hidden sm:inline">Research Library</span>
          </Button>

          <Button
            variant="clinical"
            size="sm"
            onClick={onOpenIngest}
            className="gap-1.5 text-xs shadow-md shadow-emerald-950/40"
          >
            <DownloadCloud className="w-4 h-4" />
            <span>Index Paper</span>
          </Button>

          <div className="h-5 w-[1px] bg-border mx-1" />

          <Button
            variant="outline"
            size="sm"
            onClick={onOpenAdmin}
            className="gap-2 text-muted-foreground hover:text-foreground border-border/80"
          >
            <Settings className="w-3.5 h-3.5" />
            <span className="hidden md:inline">Infrastructure & DLQ</span>
          </Button>
        </div>
      </div>
    </header>
  );
}
