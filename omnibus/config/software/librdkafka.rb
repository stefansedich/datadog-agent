# https://github.com/confluentinc/librdkafka#build-from-source
# https://github.com/Homebrew/homebrew-core/blob/35f8763a90eab4203deb3a6ee2503cf0ddfd8c84/Formula/librdkafka.rb#L32
# https://github.com/confluentinc/confluent-kafka-python/blob/master/tools/windows-install-librdkafka.bat

name "librdkafka"
default_version "2.1.1"

dependency "cyrus-sasl"

source :url => "https://github.com/confluentinc/librdkafka/archive/refs/tags/v#{version}.tar.gz",
        :sha256 => "7be1fc37ab10ebdc037d5c5a9b35b48931edafffae054b488faaff99e60e0108",
        :extract => :seven_zip

relative_path "librdkafka-#{version}"

build do

  license "BSD-style"
  license_file "https://raw.githubusercontent.com/confluentinc/librdkafka/master/LICENSE"

  env = {
    "LDFLAGS" => "-L#{install_dir}/embedded/lib -I#{install_dir}/embedded/include",
    "CFLAGS" => "-L#{install_dir}/embedded/lib -I#{install_dir}/embedded/include",
    "LD_RUN_PATH" => "#{install_dir}/embedded/lib",
  }

  command "./configure --enable-sasl --prefix=#{install_dir}/embedded", :env => env
  command "make", :env => env
  command "make install", :env => env

  delete "#{install_dir}/embedded/lib/librdkafka.a"
  delete "#{install_dir}/embedded/lib/librdkafka-static.a"

end
