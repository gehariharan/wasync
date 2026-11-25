require "pathname"
require "time"

module OS
  def self.linux?
    Formula.platform == :linux
  end
end

class Formula
  class << self
    attr_accessor :platform
    attr_reader :metadata, :dependencies

    def inherited(child)
      child.instance_variable_set(:@metadata, {})
      child.instance_variable_set(:@dependencies, [])
      child.platform = platform
    end

    [:desc, :homepage, :url, :version, :sha256, :license].each do |key|
      define_method(key) { |value| @metadata[key] = value }
    end

    def depends_on(value)
      @dependencies << value
    end

    def deprecate!(date:, because:)
      @metadata[:deprecate] = {date: date, because: because}
    end

    def [](name)
      Struct.new(:opt_bin).new(Pathname("/brew/opt/#{name.split('/').last}/bin"))
    end

    def on_linux
      yield if platform == :linux
    end

    def livecheck(&block)
      settings = {}
      checker = Object.new
      [:url, :strategy].each do |key|
        checker.define_singleton_method(key) { |value| settings[key] = value }
      end
      checker.instance_eval(&block)
      @metadata[:livecheck] = settings
    end

    def test
    end
  end

  attr_reader :commands, :ldflags

  def initialize
    @commands = []
    @directory = "."
  end

  def version
    self.class.metadata.fetch(:version)
  end

  def time
    Time.utc(2026, 10, 20)
  end

  def std_go_args(ldflags:)
    @ldflags = ldflags
    ["-ldflags", ldflags.join(" ")]
  end

  def system(*args)
    @commands << [@directory, args]
  end

  def bin
    commands = @commands
    Object.new.tap { |b| b.define_singleton_method(:install_symlink) { |links| commands << [:link, links] } }
  end

  def cd(directory)
    previous, @directory = @directory, directory
    yield
  ensure
    @directory = previous
  end
end

work, version, checksum, commit = ARGV
[:linux, :macos].each do |platform|
  Formula.platform = platform
  {"wasync-axi.rb" => ["WasyncAxi", "axi", "wasync-axi", commit],
   "no-commit.rb" => ["WasyncAxi", "axi", "wasync-axi", nil],
   "combined/wasync-axi.rb" => ["WasyncAxi", "axi", "wasync-axi", commit],
   "combined/wasync-formula.rb" => ["Wasync", ".", "wasync", commit]}.each do |file, (name, directory, binary, revision)|
    Object.send(:remove_const, name) if Object.const_defined?(name)
    load File.join(work, file)
    klass = Object.const_get(name)
    metadata = klass.metadata
    expected_url = "https://api.github.com/repos/gehariharan/wasync/tarball/v#{version}"
    raise "wrong release metadata: #{file}" unless metadata.values_at(:url, :version, :sha256, :license) == [expected_url, version, checksum, "MIT"]
    if binary == "wasync-axi"
      raise "wrong livecheck configuration" unless metadata[:livecheck] == {url: "https://github.com/gehariharan/wasync", strategy: :github_latest}
    end
    expected_deps = [{"go" => :build}]
    if binary == "wasync"
      expected_deps.unshift(:linux)
    elsif platform == :linux
      expected_deps << "gehariharan/tap/wasync"
    end
    raise "wrong #{platform} dependencies: #{klass.dependencies.inspect}" unless klass.dependencies == expected_deps
    formula = klass.new
    ENV["GOTOOLCHAIN"] = "local"
    formula.install
    if binary == "wasync-axi"
      caveat = formula.caveats
      raise "Linux caveat recommends a Mac cask" if platform == :linux && caveat.include?("--cask")
      raise "Mac caveat omitted signed cask" if platform == :macos && !caveat.include?("brew install --cask gehariharan/tap/wasync")
    end
    prefix = "github.com/gehariharan/wasync/#{binary == 'wasync-axi' ? 'axi/' : ''}internal/buildinfo"
    flags = ["-s", "-w", "-X", "#{prefix}.Version=#{version}"]
    flags += ["-X", "#{prefix}.Commit=#{revision}"] if revision
    flags += ["-X", "#{prefix}.#{binary == 'wasync-axi' ? 'Date' : 'BuiltAt'}=2026-10-20T00:00:00Z"]
    raise "wrong build metadata: #{formula.ldflags.inspect}" unless formula.ldflags == flags
    args = ["go", "build", "-ldflags", flags.join(" ")]
    args += ["-tags", "sqlite_fts5"] if binary == "wasync"
    args << "./cmd/#{binary}"
    raise "wrong build invocation: #{formula.commands.inspect}" unless formula.commands == [[directory, args]]
    raise "wrong build environment" unless ENV["CGO_ENABLED"] == (binary == "wasync" ? "1" : "0") && ENV["GOWORK"] == "off" && ENV["GOTOOLCHAIN"] == "auto"
  end
end

# The old name, whatsapp-axi, builds nothing: it is deprecated, depends on
# wasync-axi and links the old command to it.
Object.send(:remove_const, "WhatsappAxi") if Object.const_defined?("WhatsappAxi")
load File.join(work, "combined/whatsapp-axi.rb")
legacy = WhatsappAxi
raise "wrong old-name release metadata" unless legacy.metadata.values_at(:url, :version, :sha256, :license) == ["https://api.github.com/repos/gehariharan/wasync/tarball/v#{version}", version, checksum, "MIT"]
raise "old name is not deprecated toward wasync-axi" unless legacy.metadata[:deprecate] && legacy.metadata[:deprecate][:because].include?("wasync-axi")
raise "wrong old-name dependencies: #{legacy.dependencies.inspect}" unless legacy.dependencies == ["gehariharan/tap/wasync-axi"]
formula = legacy.new
formula.install
raise "wrong old-name install: #{formula.commands.inspect}" unless formula.commands == [[:link, {Pathname("/brew/opt/wasync-axi/bin/wasync-axi") => "whatsapp-axi"}]]
